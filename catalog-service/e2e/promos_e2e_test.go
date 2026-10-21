//go:build e2e

package e2e

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkPromosStates(t *testing.T) {
	for _, tc := range []struct {
		id, name, kind, expected string
		value                    int
	}{
		{"PROMO-001", "Percentage discount at allowed cap", "PERCENTAGE", "30", 70},
		{"PROMO-002", "Percentage discount above cap is ignored", "PERCENTAGE", "100", 71},
		{"PROMO-003", "Fixed discount cannot make total negative", "FIXED_AMOUNT", "0", 200},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Promos and order states", tc.name, func(s *scenario) {
				buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
				discount := s.promo(M{"discount_value": tc.value, "discount_type": tc.kind})
				s.step("Apply promo according to discount policy", func() {
					order := s.order(buyer, product, 1, discount)
					require.Equal(s.t, tc.expected, amount(order["total_amount"]).String())
					s.cancel(buyer, order)
				})
			})
		})
	}
	runCase(t, "PROMO-004", "Promos and order states", "Update below promo minimum removes discount and releases usage", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
		discount := s.promo(M{"min_order_amount": 150})
		order := s.order(buyer, product, 2, discount)
		s.step("Reduce order below minimum and detach promo", func() {
			updated := s.request("PUT", pathID("/orders", integer(order["id"])), items(product, 1), buyer.Token, 200, "")
			require.Nil(s.t, updated["promo_code_id"])
			require.Equal(s.t, "0", amount(updated["discount_amount"]).String())
			require.Equal(s.t, int64(0), s.uses(discount))
			s.cancel(buyer, order)
		})
	})
	for _, tc := range []struct{ id, name, mutation string }{
		{"PROMO-005", "Deactivated promo rejects update atomically", "active=false"},
		{"PROMO-006", "Expired promo rejects update atomically", "valid_until=NOW()-interval '1 second'"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Promos and order states", tc.name, func(s *scenario) {
				buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
				discount := s.promo(nil)
				order := s.order(buyer, product, 1, discount)
				s.step("Invalidate promo after reserving its usage", func() {
					s.sql(fmt.Sprintf("UPDATE promo_codes SET %s WHERE id=%d", tc.mutation, integer(discount["id"])))
				})
				s.step("Failed update preserves stock, order, items, operations and usage", func() {
					before := s.snapshot(buyer, product, order)
					s.request("PUT", pathID("/orders", integer(order["id"])), items(product, 2), buyer.Token, 422, "PROMO_CODE_INVALID")
					require.Equal(s.t, before, s.snapshot(buyer, product, order))
					require.Equal(s.t, int64(1), s.uses(discount))
					s.cancel(buyer, order)
				})
			})
		})
	}
	runCase(t, "PROMO-007", "Promos and order states", "Admin advances order state and terminal states retain reservation", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, nil)
		order := s.order(buyer, product, 1, nil)
		path := pathID("/orders", integer(order["id"]))
		s.step("Reject skipped state and buyer status mutation", func() {
			s.request("POST", path+"/status", M{"status": "PAID"}, s.admin(), 409, "INVALID_STATE_TRANSITION")
			s.request("POST", path+"/status", M{"status": "PAYMENT_PENDING"}, buyer.Token, 403, "ACCESS_DENIED")
		})
		for _, state := range []string{"PAYMENT_PENDING", "PAID", "SHIPPED", "COMPLETED"} {
			s.step("Advance to "+state+" and enforce allowed mutations", func() {
				updated := s.request("POST", path+"/status", M{"status": state}, s.admin(), 200, "")
				require.Equal(s.t, state, updated["status"])
				s.request("PUT", path, items(product, 1), buyer.Token, 409, "INVALID_STATE_TRANSITION")
				if state != "PAYMENT_PENDING" {
					s.request("POST", path+"/cancel", nil, buyer.Token, 409, "INVALID_STATE_TRANSITION")
				}
			})
		}
		s.step("Completed order keeps consumed stock", func() {
			require.Equal(s.t, int64(19), s.stock(product))
		})
	})
	runCase(t, "PROMO-008", "Promos and order states", "Payment-pending order remains cancelable", func(s *scenario) {
		buyer, product := s.user("USER"), s.product(s.seller().Token, M{"stock": 19})
		order := s.order(buyer, product, 1, nil)
		s.step("Cancel from payment pending and restore reservation", func() {
			s.request("POST", pathID("/orders", integer(order["id"]))+"/status", M{"status": "PAYMENT_PENDING"}, s.admin(), 200, "")
			s.cancel(buyer, order)
			require.Equal(s.t, int64(19), s.stock(product))
		})
	})
}
