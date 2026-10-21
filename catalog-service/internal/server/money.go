package server

import (
	"net/http"

	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"github.com/shopspring/decimal"
)

func validateMoney(w http.ResponseWriter, input any) bool {
	fields := map[string]decimal.Decimal{}
	switch value := input.(type) {
	case *api.ProductCreate:
		fields["price"] = value.Price
	case *api.ProductUpdate:
		fields["price"] = value.Price
	case *api.PromoCodeCreate:
		fields["discount_value"] = value.DiscountValue
		fields["min_order_amount"] = value.MinOrderAmount
	}
	violations := map[string]any{}
	maximum := decimal.RequireFromString("9999999999.99")
	for name, value := range fields {
		if value.IsZero() {
			if name != "min_order_amount" {
				violations[name] = "Must be at least 0.01"
			}
			continue
		}
		// Reject extreme exponents before decimal arithmetic can allocate huge powers of ten.
		adjusted := int64(value.NumDigits()) + int64(value.Exponent())
		if value.IsNegative() || adjusted < -1 || adjusted > 10 || value.GreaterThan(maximum) || !value.Equal(value.Truncate(2)) {
			minimum := "0.01"
			if name == "min_order_amount" {
				minimum = "0"
			}
			violations[name] = "Must be between " + minimum + " and 9999999999.99, with at most two decimal places"
		}
	}
	if len(violations) > 0 {
		writeError(w, 400, "VALIDATION_ERROR", "Invalid monetary amount", violations)
		return false
	}
	return true
}
