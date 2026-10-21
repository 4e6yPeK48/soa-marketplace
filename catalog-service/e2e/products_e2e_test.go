//go:build e2e

package e2e

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func checkProducts(t *testing.T) {
	runCase(t, "CAT-001", "products", "A buyer cannot create a product", func(s *scenario) {
		var buyer user
		s.precondition("Register a buyer", func() { buyer = s.buyer() })
		s.step("Reject product creation by the buyer role", func() {
			s.request("POST", "/products", M{"name": "Denied-" + s.run.id, "price": 1, "stock": 1, "category": s.run.id, "status": "ACTIVE"}, buyer.Token, 403, "ACCESS_DENIED")
		})
	})
	for i, method := range []string{"POST", "PUT"} {
		runCase(t, fmt.Sprintf("CAT-%03d", i+2), "products", "Administrator cannot "+method+" a product for a nonexistent seller", func(s *scenario) {
			var token string
			var p M
			s.precondition("Create an administrator-owned product", func() {
				token = s.admin()
				p = s.product(token, nil)
			})
			s.step("Reject the nonexistent owner and preserve the existing product", func() {
				path := "/products"
				if method == "PUT" {
					path = pathID(path, integer(p["id"]))
				}
				out := s.request(method, path, M{"name": "Denied-" + s.run.id, "price": 1, "stock": 1, "category": s.run.id, "status": "ACTIVE", "seller_id": int64(9223372036854775807)}, token, 400, "VALIDATION_ERROR")
				require.NotEmpty(s.t, object(out["details"])["seller_id"])
				stored := s.request("GET", pathID("/products", integer(p["id"])), nil, token, 200, "")
				require.Equal(s.t, int64(20), integer(stored["stock"]))
			})
		})
	}
	for roleIndex, role := range []string{"SELLER", "USER"} {
		for methodIndex, method := range []string{"PUT", "DELETE"} {
			runCase(t, fmt.Sprintf("CAT-%03d", 4+roleIndex*2+methodIndex), "products", role+" cannot "+method+" another seller's product", func(s *scenario) {
				var p M
				var other user
				s.precondition("Create a product and a separate requesting user", func() {
					p = s.product(s.seller().Token, nil)
					other = s.user(role)
				})
				s.step("Reject mutation outside the seller ownership boundary", func() {
					var body any
					if method == "PUT" {
						body = M{"name": "Denied-" + s.run.id, "price": 1, "stock": 1, "category": s.run.id, "status": "ACTIVE"}
					}
					s.request(method, pathID("/products", integer(p["id"])), body, other.Token, 403, "ACCESS_DENIED")
				})
			})
		}
	}
	runCase(t, "CAT-008", "products", "Seller updates are visible in filtered paginated product lists", func(s *scenario) {
		var p M
		var buyer user
		category := s.run.id + "-CAT-008"
		s.precondition("Create an active product in an isolated category and a buyer", func() {
			p = s.product(s.seller().Token, M{"category": category})
			buyer = s.buyer()
		})
		s.step("The owner changes product name and status", func() {
			s.replaceProduct(p, M{"name": "Updated-" + s.run.id, "status": "INACTIVE"})
		})
		s.step("Filter inactive products and inspect both pagination boundaries", func() {
			page := s.request("GET", "/products?category="+category+"&status=INACTIVE&page=0&size=1", nil, buyer.Token, 200, "")
			require.Equal(s.t, int64(1), integer(page["totalElements"]))
			require.Equal(s.t, int64(0), integer(page["page"]))
			require.Equal(s.t, int64(1), integer(page["size"]))
			products := array(page["items"])
			require.Len(s.t, products, 1)
			require.Equal(s.t, integer(p["id"]), integer(object(products[0])["id"]))
			next := s.request("GET", "/products?category="+category+"&status=INACTIVE&page=1&size=1", nil, buyer.Token, 200, "")
			require.Empty(s.t, array(next["items"]))
		})
	})
	runCase(t, "CAT-009", "products", "Product lists default to twenty entries per page", func(s *scenario) {
		var seller user
		s.precondition("Register a seller", func() { seller = s.seller() })
		s.step("Inspect the default pagination size", func() {
			page := s.request("GET", "/products", nil, seller.Token, 200, "")
			require.Equal(s.t, int64(20), integer(page["size"]))
		})
	})
	runCase(t, "CAT-010", "products", "Administrator deletion archives a product without hiding it", func(s *scenario) {
		var p M
		var buyer user
		var admin string
		s.precondition("Create a seller product, buyer, and administrator credentials", func() {
			p = s.product(s.seller().Token, nil)
			buyer, admin = s.buyer(), s.admin()
		})
		s.step("Archive the product and verify database and API state", func() {
			id := integer(p["id"])
			path := pathID("/products", id)
			s.request("DELETE", path, nil, admin, 204, "")
			require.Equal(s.t, "ARCHIVED", s.sql(fmt.Sprintf("SELECT status FROM products WHERE id=%d", id)))
			stored := s.request("GET", path, nil, buyer.Token, 200, "")
			require.Equal(s.t, "ARCHIVED", stored["status"])
		})
	})
	runCase(t, "CAT-011", "products", "Missing product IDs return a product error", func(s *scenario) {
		var buyer user
		s.precondition("Register a buyer", func() { buyer = s.buyer() })
		s.step("Read a nonexistent product", func() {
			s.request("GET", "/products/9223372036854775807", nil, buyer.Token, 404, "PRODUCT_NOT_FOUND")
		})
	})
	runCase(t, "CAT-012", "products", "Sellers cannot create orders", func(s *scenario) {
		var p M
		var seller user
		s.precondition("Register a seller and create its product", func() {
			seller = s.seller()
			p = s.product(seller.Token, nil)
		})
		s.step("Reject order creation by the seller role", func() {
			s.request("POST", "/orders", items(p, 1), seller.Token, 403, "ACCESS_DENIED")
		})
	})
	runCase(t, "CAT-013", "products", "Buyers cannot create promo codes", func(s *scenario) {
		var buyer user
		s.precondition("Register a buyer", func() { buyer = s.buyer() })
		s.step("Reject promo creation by the buyer role", func() {
			now := time.Now().UTC()
			s.request("POST", "/promo-codes", M{"code": "DENIED_" + s.run.id, "discount_type": "PERCENTAGE", "discount_value": 10, "min_order_amount": 0, "max_uses": 1, "valid_from": now.Add(-24 * time.Hour).Format(time.RFC3339Nano), "valid_until": now.Add(24 * time.Hour).Format(time.RFC3339Nano)}, buyer.Token, 403, "ACCESS_DENIED")
		})
	})
}
