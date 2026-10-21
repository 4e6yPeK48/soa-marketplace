//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkValidation(t *testing.T) {
	for i, tc := range []struct {
		name    string
		changes M
	}{
		{"Missing product fields", nil},
		{"Empty product name", M{"name": ""}},
		{"Product name exceeds 255 characters", M{"name": strings.Repeat("x", 256)}},
		{"Product description exceeds 4000 characters", M{"description": strings.Repeat("x", 4001)}},
		{"Zero product price", M{"price": 0}},
		{"Product price has three decimal places", M{"price": json.Number("1.001")}},
		{"Negative product stock", M{"stock": -1}},
		{"Empty product category", M{"category": ""}},
		{"Product category exceeds 100 characters", M{"category": strings.Repeat("x", 101)}},
		{"Unknown product status", M{"status": "BAD"}},
		{"Null product name", M{"name": nil}},
	} {
		runCase(t, fmt.Sprintf("VAL-%03d", i+1), "validation", tc.name, func(s *scenario) {
			var seller user
			s.precondition("Register a seller", func() { seller = s.seller() })
			s.step("Reject invalid product fields with violation details", func() {
				body := M{}
				if tc.changes != nil {
					body = M{"name": "Valid-" + s.run.id, "price": 1, "stock": 1, "category": s.run.id, "status": "ACTIVE"}
					for key, value := range tc.changes {
						body[key] = value
					}
				}
				out := s.request("POST", "/products", body, seller.Token, 400, "VALIDATION_ERROR")
				require.NotEmpty(s.t, out["details"])
			})
		})
	}
	for i, query := range []string{"page=-1", "size=0", "size=101", "status=BAD"} {
		runCase(t, fmt.Sprintf("VAL-%03d", 12+i), "validation", "Reject product query "+query, func(s *scenario) {
			var buyer user
			s.precondition("Register a buyer", func() { buyer = s.buyer() })
			s.step("Reject an invalid catalog query", func() {
				s.request("GET", "/products?"+query, nil, buyer.Token, 400, "VALIDATION_ERROR")
			})
		})
	}
	for i, tc := range []struct {
		name  string
		input M
	}{
		{"Missing order items", M{}},
		{"Empty order items", M{"items": []any{}}},
		{"Null order items", M{"items": nil}},
		{"Zero order quantity", M{"items": []any{M{"product_id": 1, "quantity": 0}}}},
		{"Order quantity exceeds 999", M{"items": []any{M{"product_id": 1, "quantity": 1000}}}},
		{"Order contains more than fifty items", nil},
		{"Order promo code contains invalid characters", M{"items": []any{M{"product_id": 1, "quantity": 1}}, "promo_code": "bad!"}},
		{"Order contains duplicate products", nil},
	} {
		runCase(t, fmt.Sprintf("VAL-%03d", 16+i), "validation", tc.name, func(s *scenario) {
			var buyer user
			var p M
			s.precondition("Create an untouched product and a fresh buyer", func() {
				p = s.product(s.seller().Token, nil)
				buyer = s.buyer()
			})
			s.step("Reject invalid order items with violation details", func() {
				body := tc.input
				if i == 5 {
					entries := make([]any, 51)
					for index := range entries {
						entries[index] = M{"product_id": 1, "quantity": 1}
					}
					body = M{"items": entries}
				}
				if i == 7 {
					body = M{"items": []any{M{"product_id": p["id"], "quantity": 1}, M{"product_id": p["id"], "quantity": 1}}}
				}
				out := s.request("POST", "/orders", body, buyer.Token, 400, "VALIDATION_ERROR")
				require.NotEmpty(s.t, out["details"])
			})
			s.step("Validation failure preserves inventory and creates no cooldown entry", func() {
				require.Equal(s.t, int64(20), s.stock(p))
				require.Equal(s.t, "0", s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", buyer.ID)))
			})
		})
	}
	runCase(t, "VAL-024", "validation", "Malformed product JSON returns field violation details", func(s *scenario) {
		var seller user
		s.precondition("Register a seller", func() { seller = s.seller() })
		s.step("Reject the malformed request body", func() {
			resp := s.raw("POST", "/products", []byte("{"), seller.Token, 400, "VALIDATION_ERROR")
			require.NotEmpty(s.t, resp.JSON["details"])
		})
	})
}
