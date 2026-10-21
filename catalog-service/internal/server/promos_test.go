package server

import (
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"github.com/shopspring/decimal"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPromoDiscount(t *testing.T) {
	for _, tc := range []struct{ kind, value, subtotal, want string }{{"PERCENTAGE", "10", "0.05", "0.01"}, {"PERCENTAGE", "70", "100", "70"}, {"PERCENTAGE", "70.01", "100", "0"}, {"FIXED_AMOUNT", "100", "9.99", "9.99"}, {"PERCENTAGE", "12.5", "19.99", "2.50"}} {
		got := promoDiscount(tc.kind, decimal.RequireFromString(tc.value), decimal.RequireFromString(tc.subtotal))
		if !got.Equal(decimal.RequireFromString(tc.want)) {
			t.Errorf("%+v got %s", tc, got)
		}
	}
}

func TestMoneyValidationExplainsBounds(t *testing.T) {
	for _, tc := range []struct{ amount, minimum string }{{"-1", "0"}, {"0.001", "0"}} {
		w := httptest.NewRecorder()
		if validateMoney(w, &api.PromoCodeCreate{DiscountValue: decimal.NewFromInt(1), MinOrderAmount: decimal.RequireFromString(tc.amount)}) {
			t.Fatal("invalid money accepted")
		}
		body := w.Body.String()
		if strings.Contains(body, "NUMERIC") || !strings.Contains(body, "between "+tc.minimum+" and 9999999999.99") || !strings.Contains(body, "two decimal places") {
			t.Fatalf("unhelpful explanation: %s", body)
		}
	}
	w := httptest.NewRecorder()
	if !validateMoney(w, &api.PromoCodeCreate{DiscountValue: decimal.NewFromInt(1), MinOrderAmount: decimal.Zero}) {
		t.Fatal("zero minimum rejected")
	}
}
