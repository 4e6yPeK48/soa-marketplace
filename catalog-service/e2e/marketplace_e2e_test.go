//go:build e2e

package e2e

import "testing"

func TestMarketplace(t *testing.T) {
	for _, group := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"PublicRoutes", checkPublicRoutes}, {"Auth", checkAuth}, {"Products", checkProducts},
		{"Validation", checkValidation}, {"OrderLifecycle", checkOrderLifecycle},
		{"OrderFailuresRollback", checkOrderFailuresRollback}, {"MoneyStockBoundaries", checkMoneyStockBoundaries},
		{"DiscountedOrderAmounts", checkDiscountedOrderAmounts}, {"PromosStates", checkPromosStates},
		{"Concurrency", checkConcurrency}, {"Logs", checkLogs},
	} {
		t.Run(group.name, group.run)
	}
}
