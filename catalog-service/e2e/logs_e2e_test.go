//go:build e2e

package e2e

import "testing"

func checkLogs(t *testing.T) {
	runCase(t, "LOG-001", "logs", "request_audit", func(s *scenario) {
		s.step("Проверяем запросы и безопасность журнала", func() {
			s.request("GET", "/products", nil, s.buyer().Token, 200, "")
			s.verifyLogs()
		})
	})
}
