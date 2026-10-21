//go:build e2e

package e2e

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkAuth(t *testing.T) {
	for i, token := range []string{"", "malformed"} {
		runCase(t, fmt.Sprintf("AUTH-%03d", i+1), "auth", fmt.Sprintf("Reject access token variant %d", i), func(s *scenario) {
			s.step("Reject an unauthenticated catalog request", func() {
				s.request("GET", "/products", nil, token, 401, "TOKEN_INVALID")
			})
		})
	}
	runCase(t, "AUTH-003", "auth", "Public registration cannot create an administrator", func(s *scenario) {
		s.step("Reject the reserved registration role", func() {
			s.request("POST", "/auth/register", M{"email": "admin-" + s.run.id + "@example.test", "password": fixturePassword, "role": "ADMIN"}, "", 400, "VALIDATION_ERROR")
		})
	})
	runCase(t, "AUTH-004", "auth", "Duplicate registration returns an email conflict", func(s *scenario) {
		var buyer user
		s.precondition("Register a buyer", func() { buyer = s.buyer() })
		s.step("Register the same email again", func() {
			s.request("POST", "/auth/register", M{"email": buyer.Email, "password": fixturePassword, "role": "USER"}, "", 409, "EMAIL_ALREADY_EXISTS")
		})
	})
	runCase(t, "AUTH-005", "auth", "A refresh token issues a usable access token", func(s *scenario) {
		var buyer user
		s.precondition("Register and authenticate a buyer", func() { buyer = s.buyer() })
		s.step("Refresh credentials and use the returned access token", func() {
			pair := s.request("POST", "/auth/refresh", M{"refresh_token": buyer.Pair["refresh_token"]}, "", 200, "")
			token, ok := pair["access_token"].(string)
			require.True(s.t, ok, "Refresh response must include an access token")
			require.NotEmpty(s.t, token)
			s.request("GET", "/products", nil, token, 200, "")
		})
	})
	runCase(t, "AUTH-006", "auth", "A refresh token cannot authenticate catalog requests", func(s *scenario) {
		var buyer user
		s.precondition("Register and authenticate a buyer", func() { buyer = s.buyer() })
		s.step("Reject the wrong token type", func() {
			token, ok := buyer.Pair["refresh_token"].(string)
			require.True(s.t, ok)
			s.request("GET", "/products", nil, token, 401, "TOKEN_INVALID")
		})
	})
	runCase(t, "AUTH-007", "auth", "An access token cannot refresh credentials", func(s *scenario) {
		var buyer user
		s.precondition("Register and authenticate a buyer", func() { buyer = s.buyer() })
		s.step("Reject the wrong refresh token type", func() {
			s.request("POST", "/auth/refresh", M{"refresh_token": buyer.Pair["access_token"]}, "", 401, "REFRESH_TOKEN_INVALID")
		})
	})
	runCase(t, "AUTH-008", "auth", "Malformed refresh tokens are rejected", func(s *scenario) {
		s.step("Reject an invalid refresh token", func() {
			s.request("POST", "/auth/refresh", M{"refresh_token": "bad"}, "", 401, "REFRESH_TOKEN_INVALID")
		})
	})
	runCase(t, "AUTH-009", "auth", "Login tokens contain the buyer role and bounded lifetime", func(s *scenario) {
		var buyer user
		s.precondition("Register and authenticate a buyer", func() { buyer = s.buyer() })
		s.step("Read claims without logging token contents", func() {
			parts := strings.Split(buyer.Token, ".")
			require.True(s.t, len(parts) == 3, "access token must have three parts")
			payload, err := base64.RawURLEncoding.DecodeString(parts[1])
			require.NoError(s.t, err)
			var claims M
			err = json.Unmarshal(payload, &claims)
			require.True(s.t, err == nil, "Access token payload must be valid JSON")
			require.Equal(s.t, "USER", claims["role"])
			require.GreaterOrEqual(s.t, integer(buyer.Pair["expires_in"]), int64(900))
			require.LessOrEqual(s.t, integer(buyer.Pair["expires_in"]), int64(1800))
		})
	})
}
