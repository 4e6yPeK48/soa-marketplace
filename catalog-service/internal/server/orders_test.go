package server

import (
	"errors"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOrderTransitions(t *testing.T) {
	for _, tc := range []struct {
		from, to api.OrderStatus
		want     bool
	}{{"CREATED", "PAYMENT_PENDING", true}, {"PAYMENT_PENDING", "PAID", true}, {"PAID", "SHIPPED", true}, {"SHIPPED", "COMPLETED", true}, {"CREATED", "PAID", false}, {"COMPLETED", "CREATED", false}, {"CANCELED", "CANCELED", false}} {
		if got := validOrderTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s: %v", tc.from, tc.to, got)
		}
	}
}

func TestDuplicateOrderItemsRejected(t *testing.T) {
	w := httptest.NewRecorder()
	if validateItems(w, []api.OrderItemInput{{ProductId: 7, Quantity: 1}, {ProductId: 7, Quantity: 2}}) {
		t.Fatal("duplicate accepted")
	}
	if w.Code != 400 || !strings.Contains(w.Body.String(), "VALIDATION_ERROR") {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
	if !validateItems(httptest.NewRecorder(), []api.OrderItemInput{{ProductId: 7, Quantity: 1}, {ProductId: 8, Quantity: 2}}) {
		t.Fatal("distinct products rejected")
	}
}

func TestCooldownRemaining(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		elapsed time.Duration
		want    int
	}{
		{"expired", 61 * time.Second, 0}, {"boundary", time.Minute, 0}, {"fraction rounded up", 59500 * time.Millisecond, 1}, {"remaining rounded up", 58500 * time.Millisecond, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := cooldownError(now.Add(-tc.elapsed), now, time.Minute)
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var problem *apiError
			if !errors.As(err, &problem) || problem.Status != 429 || problem.Details["retry_after_seconds"] != tc.want {
				t.Fatalf("unexpected cooldown error: %#v", err)
			}
		})
	}
}
