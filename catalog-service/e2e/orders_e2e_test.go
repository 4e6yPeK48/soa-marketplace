//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkOrderLifecycle(t *testing.T) {
	runCase(t, "ORD-001", "Orders", "Order ownership, cooldown, price snapshot and cancellation", func(s *scenario) {
		var buyer, stranger user
		var product, discount, order M
		s.precondition("Create independent buyer, stranger, product and single-use promo", func() {
			buyer, stranger = s.user("USER"), s.user("USER")
			product, discount = s.product(s.seller().Token, nil), s.promo(M{"max_uses": 1})
		})
		s.step("Create order and record cooldown at write time", func() {
			order = s.order(buyer, product, 2, discount)
			require.Equal(s.t, int64(18), s.stock(product))
			require.Equal(s.t, int64(1), s.uses(discount))
			require.Equal(s.t, "160", amount(order["total_amount"]).String())
			require.Equal(s.t, "40", amount(order["discount_amount"]).String())
			require.Equal(s.t, "CREATE_ORDER", s.sql(fmt.Sprintf("SELECT operation_type FROM user_operations WHERE user_id=%d", buyer.ID)))
			require.Equal(s.t, "t", s.sql(fmt.Sprintf("SELECT u.created_at > o.created_at FROM user_operations u JOIN orders o ON o.user_id=u.user_id WHERE o.id=%d AND u.operation_type='CREATE_ORDER'", integer(order["id"]))), "create cooldown starts before operation is recorded")
			s.request("POST", "/orders", items(product, 1), buyer.Token, 429, "ORDER_LIMIT_EXCEEDED")
			s.age(buyer)
			s.request("POST", "/orders", items(product, 1), buyer.Token, 409, "ORDER_HAS_ACTIVE")
		})
		s.step("Enforce ownership and seller restrictions while allowing admin read", func() {
			path := pathID("/orders", integer(order["id"]))
			for _, operation := range []struct {
				method, path string
				body         any
			}{{"GET", path, nil}, {"PUT", path, items(product, 1)}, {"POST", path + "/cancel", nil}} {
				s.request(operation.method, operation.path, operation.body, stranger.Token, 403, "ORDER_OWNERSHIP_VIOLATION")
				s.request(operation.method, operation.path, operation.body, s.seller().Token, 403, "ACCESS_DENIED")
			}
			s.request("GET", path, nil, s.admin(), 200, "")
		})
		s.step("Keep saved price until update, then recalculate reservation and promo", func() {
			s.replaceProduct(product, M{"price": 125, "stock": 18})
			path := pathID("/orders", integer(order["id"]))
			stored := s.request("GET", path, nil, buyer.Token, 200, "")
			require.Equal(s.t, "100", amount(object(array(stored["items"])[0])["price_at_order"]).String())
			updated := s.request("PUT", path, items(product, 3), buyer.Token, 200, "")
			require.Equal(s.t, int64(17), s.stock(product))
			require.Equal(s.t, int64(1), s.uses(discount))
			require.Equal(s.t, "125", amount(object(array(updated["items"])[0])["price_at_order"]).String())
			require.Equal(s.t, "300", amount(updated["total_amount"]).String())
			s.request("PUT", path, items(product, 1), buyer.Token, 429, "ORDER_LIMIT_EXCEEDED")
			require.Equal(s.t, "1", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d AND operation_type='UPDATE_ORDER'", buyer.ID)))
			require.Equal(s.t, "t", s.sql(fmt.Sprintf("SELECT u.created_at >= o.updated_at FROM user_operations u JOIN orders o ON o.user_id=u.user_id WHERE o.id=%d AND u.operation_type='UPDATE_ORDER'", integer(order["id"]))), "update cooldown starts before order is saved")
		})
		s.step("Cancel once and release stock and promo usage", func() {
			require.Equal(s.t, "CANCELED", s.cancel(buyer, order)["status"])
			require.Equal(s.t, int64(20), s.stock(product))
			require.Equal(s.t, int64(0), s.uses(discount))
			s.request("POST", pathID("/orders", integer(order["id"]))+"/cancel", nil, buyer.Token, 409, "INVALID_STATE_TRANSITION")
			s.request("GET", "/orders/9223372036854775807", nil, buyer.Token, 404, "ORDER_NOT_FOUND")
		})
	})
}

func checkOrderFailuresRollback(t *testing.T) {
	for _, tc := range []struct {
		id, name, code    string
		status, quantity  int
		inactive, missing bool
	}{
		{"ORD-002", "Inactive product leaves no order or reservation", "PRODUCT_INACTIVE", 409, 1, true, false},
		{"ORD-003", "Missing product leaves no order or reservation", "PRODUCT_NOT_FOUND", 404, 1, false, true},
		{"ORD-004", "Insufficient stock leaves no order or reservation", "INSUFFICIENT_STOCK", 409, 21, false, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Orders", tc.name, func(s *scenario) {
				buyer := s.user("USER")
				product := s.product(s.seller().Token, nil)
				candidate := product
				if tc.inactive {
					candidate = s.product(s.seller().Token, M{"status": "INACTIVE"})
				}
				if tc.missing {
					candidate = M{"id": int64(9223372036854775807)}
				}
				s.step("Reject order and leave persistent state unchanged", func() {
					out := s.request("POST", "/orders", items(candidate, tc.quantity), buyer.Token, tc.status, tc.code)
					if tc.code == "INSUFFICIENT_STOCK" {
						require.NotEmpty(s.t, out["details"])
					}
					require.Equal(s.t, int64(20), s.stock(product))
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM orders WHERE user_id=%d", buyer.ID)))
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", buyer.ID)))
				})
			})
		})
	}
	for _, tc := range []struct {
		id, name, code string
		changes        M
		unknown        bool
	}{
		{"ORD-005", "Inactive promo does not consume stock or usage", "PROMO_CODE_INVALID", M{"active": false}, false},
		{"ORD-006", "Promo minimum rejects order without side effects", "PROMO_CODE_MIN_AMOUNT", M{"min_order_amount": 101}, false},
		{"ORD-007", "Unknown promo does not create order", "PROMO_CODE_INVALID", nil, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Orders", tc.name, func(s *scenario) {
				buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
				var discount M
				body := items(product, 1)
				if tc.unknown {
					body["promo_code"] = "UNKNOWN_123"
				} else {
					discount = s.promo(tc.changes)
					body["promo_code"] = discount["code"]
				}
				s.step("Reject promo and roll back order transaction", func() {
					s.request("POST", "/orders", body, buyer.Token, 422, tc.code)
					require.Equal(s.t, int64(20), s.stock(product))
					if discount != nil {
						require.Equal(s.t, int64(0), s.uses(discount))
					}
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM orders WHERE user_id=%d", buyer.ID)))
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", buyer.ID)))
				})
			})
		})
	}
	runCase(t, "ORD-008", "Orders", "Amount and stock failures preserve order transaction", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
		expensive := s.product(s.seller().Token, M{"price": json.Number("9999999999.99"), "stock": 2})
		s.step("Reject amount overflow without recording an order or cooldown", func() {
			out := s.request("POST", "/orders", items(expensive, 2), buyer.Token, 400, "VALIDATION_ERROR")
			require.NotEmpty(s.t, object(out["details"])["items"])
			require.Equal(s.t, int64(2), s.stock(expensive))
			require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", buyer.ID)))
			require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM orders WHERE user_id=%d", buyer.ID)))
		})
		s.step("Failed update preserves stock, order, items and operation count", func() {
			order := s.order(buyer, product, 2, nil)
			before := s.snapshot(buyer, product, order)
			s.request("PUT", pathID("/orders", integer(order["id"])), items(product, 21), buyer.Token, 409, "INSUFFICIENT_STOCK")
			require.Equal(s.t, before, s.snapshot(buyer, product, order), "failed update leaked stock/order/items/operation")
			s.cancel(buyer, order)
		})
	})
}

func checkMoneyStockBoundaries(t *testing.T) {
	runCase(t, "MNY-001", "Money and stock", "Decimal prices remain exact in API and database", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, M{"price": json.Number("0.29")})
		s.step("Reserve three items at exactly 0.29", func() {
			order := s.order(buyer, product, 3, nil)
			require.Equal(s.t, "0.87", amount(order["total_amount"]).String())
			require.Equal(s.t, "0.29", amount(object(array(order["items"])[0])["price_at_order"]).String())
			require.Equal(s.t, "0.87", s.sql(fmt.Sprintf("SELECT total_amount FROM orders WHERE id=%d", integer(order["id"]))))
			s.cancel(buyer, order)
		})
	})
	runCase(t, "MNY-002", "Money and stock", "Cancellation can restore stock beyond int32", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, M{"stock": 1})
		s.step("Restock to int32 maximum before releasing an existing reservation", func() {
			order := s.order(buyer, product, 1, nil)
			require.Equal(s.t, int64(0), s.stock(product))
			s.replaceProduct(product, M{"stock": int64(2147483647)})
			s.cancel(buyer, order)
			require.Equal(s.t, int64(2147483648), s.stock(product))
			out := s.request("GET", pathID("/products", integer(product["id"])), nil, buyer.Token, 200, "")
			require.Equal(s.t, int64(2147483648), integer(out["stock"]))
		})
	})
}

func checkDiscountedOrderAmounts(t *testing.T) {
	runCase(t, "MNY-003", "Money and stock", "Validate stored amounts after discount, allowing large subtotal", func(s *scenario) {
		buyer := s.user("USER")
		product, discount := s.product(s.seller().Token, M{"price": int64(6000000000)}), s.promo(M{"discount_value": 50})
		var order M
		s.step("Create order with subtotal above limit and valid discounted amounts", func() {
			order = s.order(buyer, product, 2, discount)
			require.Equal(s.t, "6000000000", amount(order["total_amount"]).String())
			require.Equal(s.t, "6000000000", amount(order["discount_amount"]).String())
			require.Equal(s.t, int64(18), s.stock(product))
			require.Equal(s.t, int64(1), s.uses(discount))
			require.Equal(s.t, "6000000000.00|6000000000.00", s.sql(fmt.Sprintf("SELECT total_amount,discount_amount FROM orders WHERE id=%d", integer(order["id"]))))
		})
		s.step("Update still validates discounted amounts rather than subtotal", func() {
			updated := s.request("PUT", pathID("/orders", integer(order["id"])), items(product, 3), buyer.Token, 200, "")
			require.Equal(s.t, "9000000000", amount(updated["total_amount"]).String())
			require.Equal(s.t, "9000000000", amount(updated["discount_amount"]).String())
			require.Equal(s.t, int64(17), s.stock(product))
			require.Equal(s.t, int64(1), s.uses(discount))
			require.Equal(s.t, "9000000000.00|9000000000.00", s.sql(fmt.Sprintf("SELECT total_amount,discount_amount FROM orders WHERE id=%d", integer(order["id"]))))
			s.cancel(buyer, order)
			require.Equal(s.t, int64(20), s.stock(product))
			require.Equal(s.t, int64(0), s.uses(discount))
		})
	})
	for _, tc := range []struct {
		id, name             string
		percentage, quantity int
	}{
		{"MNY-004", "Total amount overflow rolls back create and update", 10, 2},
		{"MNY-005", "Discount amount overflow rolls back create and update", 70, 3},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Money and stock", tc.name, func(s *scenario) {
				buyer := s.user("USER")
				product, discount := s.product(s.seller().Token, M{"price": int64(6000000000)}), s.promo(M{"discount_value": tc.percentage})
				s.step("Reject independently overflowing stored amount without side effects", func() {
					body := items(product, tc.quantity)
					body["promo_code"] = discount["code"]
					s.request("POST", "/orders", body, buyer.Token, 400, "VALIDATION_ERROR")
					require.Equal(s.t, int64(20), s.stock(product))
					require.Equal(s.t, int64(0), s.uses(discount))
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM orders WHERE user_id=%d", buyer.ID)))
					require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", buyer.ID)))
				})
				s.step("Failed update preserves a previously valid discounted order", func() {
					order := s.order(buyer, product, 1, discount)
					before := s.snapshot(buyer, product, order)
					s.request("PUT", pathID("/orders", integer(order["id"])), items(product, tc.quantity), buyer.Token, 400, "VALIDATION_ERROR")
					require.Equal(s.t, before, s.snapshot(buyer, product, order), "amount overflow leaked stock/order/items/operation")
					require.Equal(s.t, int64(1), s.uses(discount))
					s.cancel(buyer, order)
					require.Equal(s.t, int64(20), s.stock(product))
					require.Equal(s.t, int64(0), s.uses(discount))
				})
			})
		})
	}
}
