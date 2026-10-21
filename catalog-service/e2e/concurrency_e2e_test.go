//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkConcurrency(t *testing.T) {
	for _, tc := range []struct {
		id, name, failure               string
		status                          int
		stockRace, sameBuyer, promoRace bool
	}{
		{"RACE-001", "Two buyers compete for the last unit", "INSUFFICIENT_STOCK", 409, true, false, false},
		{"RACE-002", "One buyer cannot concurrently create two active orders", "ORDER_LIMIT_EXCEEDED", 429, false, true, false},
		{"RACE-003", "Two buyers compete for the final promo usage", "PROMO_CODE_INVALID", 422, false, false, true},
	} {
		t.Run(tc.id, func(t *testing.T) {
			runCase(t, tc.id, "Concurrency", tc.name, func(s *scenario) {
				changes := M{}
				if tc.stockRace {
					changes["stock"] = 1
				}
				product := s.product(s.seller().Token, changes)
				buyers := []user{s.user("USER")}
				if tc.sameBuyer {
					buyers = append(buyers, buyers[0])
				} else {
					buyers = append(buyers, s.user("USER"))
				}
				var discount M
				body := items(product, 1)
				if tc.promoRace {
					discount = s.promo(M{"max_uses": 1})
					body["promo_code"] = discount["code"]
				}
				encoded, err := json.Marshal(body)
				require.NoError(s.t, err)
				var winner user
				var winningOrder M
				s.step("Race requests and observe results on the parent goroutine", func() {
					type result struct {
						buyer    user
						response response
						err      error
					}
					results := make(chan result, len(buyers))
					start := make(chan struct{})
					for _, buyer := range buyers {
						go func(buyer user) {
							<-start
							out, err := s.exchange("POST", "/orders", encoded, buyer.Token)
							results <- result{buyer, out, err}
						}(buyer)
					}
					close(start)
					collected := make([]result, 0, len(buyers))
					for range buyers {
						collected = append(collected, <-results)
					}
					for _, attempt := range collected {
						if attempt.err == nil && attempt.response.Status == 201 {
							s.rememberOrder(integer(attempt.response.JSON["id"]))
						}
					}
					for _, attempt := range collected {
						s.observe("POST", "/orders", encoded, attempt.buyer.Token, attempt.response, attempt.err)
					}
					wins, losses := 0, 0
					for _, attempt := range collected {
						if attempt.response.Status == 201 {
							wins++
							winner, winningOrder = attempt.buyer, attempt.response.JSON
						} else {
							losses++
							require.Equal(s.t, tc.status, attempt.response.Status)
							require.Equal(s.t, tc.failure, attempt.response.JSON["error_code"])
							require.NotEmpty(s.t, attempt.response.JSON["message"])
							if tc.status == 429 {
								seconds := integer(object(attempt.response.JSON["details"])["retry_after_seconds"])
								require.GreaterOrEqual(s.t, seconds, int64(1))
								retry, err := strconv.ParseInt(attempt.response.Header.Get("Retry-After"), 10, 64)
								require.NoError(s.t, err)
								require.Equal(s.t, seconds, retry)
							}
						}
					}
					require.Equal(s.t, 1, wins, "race must have exactly one winner")
					require.Equal(s.t, 1, losses, "race must have exactly one business rejection")
				})
				s.step("Check atomic reservation and release the winning order", func() {
					expectedStock := int64(19)
					if tc.stockRace {
						expectedStock = 0
					}
					require.Equal(s.t, expectedStock, s.stock(product))
					if tc.sameBuyer {
						require.Equal(s.t, "1", s.sql(fmt.Sprintf("SELECT count(*) FROM orders WHERE user_id=%d AND status IN ('CREATED','PAYMENT_PENDING')", winner.ID)))
					}
					if tc.promoRace {
						require.Equal(s.t, int64(1), s.uses(discount))
					}
					s.cancel(winner, winningOrder)
					if tc.stockRace {
						require.Equal(s.t, int64(1), s.stock(product))
					}
					if tc.promoRace {
						require.Equal(s.t, int64(20), s.stock(product))
						require.Equal(s.t, int64(0), s.uses(discount))
					}
				})
			})
		})
	}
}
