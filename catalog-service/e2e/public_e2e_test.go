//go:build e2e

package e2e

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func checkPublicRoutes(t *testing.T) {
	for i, tc := range []struct{ method, path string }{
		{"GET", "/"}, {"HEAD", "/"}, {"GET", "/health"}, {"GET", "/openapi.yaml"},
	} {
		runCase(t, fmt.Sprintf("PUB-%03d", i+1), "public", tc.method+" "+tc.path, func(s *scenario) {
			s.step("Read the public response and its content headers", func() {
				resp := s.raw(tc.method, tc.path, nil, "", 200, "")
				switch {
				case tc.method == "HEAD":
					require.Empty(s.t, resp.Body)
				case tc.path == "/":
					require.Contains(s.t, resp.Header.Get("Content-Type"), "text/html")
					html := strings.ToLower(string(resp.Body))
					require.True(s.t, strings.Contains(html, "<html") || strings.Contains(html, "<!doctype html"))
				case tc.path == "/health":
					require.Equal(s.t, "ok\n", string(resp.Body))
					require.Contains(s.t, resp.Header.Get("Content-Type"), "text/plain")
				default:
					require.Contains(s.t, string(resp.Body), "openapi:")
					require.Contains(s.t, resp.Header.Get("Content-Disposition"), "attachment")
				}
			})
		})
	}
	for tokenIndex, token := range []string{"", "malformed"} {
		for pathIndex, path := range []string{"/missing", "/auth/nope", "/products/1/nope", "/swagger/missing", "/swagger/missing.js"} {
			runCase(t, fmt.Sprintf("PUB-%03d", 5+tokenIndex*5+pathIndex), "public", fmt.Sprintf("Unknown route %s with token variant %d", path, tokenIndex), func(s *scenario) {
				s.step("Return a routing error before authentication", func() {
					s.request("GET", path, nil, token, 404, "ROUTE_NOT_FOUND")
				})
			})
		}
	}
	runCase(t, "PUB-015", "public", "Products collection rejects DELETE", func(s *scenario) {
		s.step("Return 405 with both supported product methods", func() {
			resp := s.raw("DELETE", "/products", nil, "", 405, "METHOD_NOT_ALLOWED")
			methods := strings.Split(resp.Header.Get("Allow"), ",")
			for i := range methods {
				methods[i] = strings.TrimSpace(methods[i])
			}
			require.Contains(s.t, methods, "GET")
			require.Contains(s.t, methods, "POST")
		})
	})
	runCase(t, "PUB-016", "public", "Swagger redirects to its trailing slash route", func(s *scenario) {
		s.step("Inspect the redirect without following it", func() {
			resp, err := s.exchange("GET", "/swagger", nil, "")
			s.observe("GET", "/swagger", nil, "", resp, err)
			require.Contains(s.t, []int{301, 308}, resp.Status)
			require.Equal(s.t, "/swagger/", resp.Header.Get("Location"))
		})
	})
	for assetIndex, asset := range []struct{ path, contentType string }{
		{"/swagger/", "text/html"},
		{"/swagger/swagger-ui.css", "text/css"},
		{"/swagger/swagger-ui-bundle.js", "javascript"},
	} {
		for tokenIndex, token := range []string{"", "malformed"} {
			runCase(t, fmt.Sprintf("PUB-%03d", 17+assetIndex*2+tokenIndex), "public", fmt.Sprintf("Swagger asset %s with token variant %d", asset.path, tokenIndex), func(s *scenario) {
				s.step("Serve the bundled public asset", func() {
					resp := s.raw("GET", asset.path, nil, token, 200, "")
					require.Contains(s.t, resp.Header.Get("Content-Type"), asset.contentType)
					require.NotEmpty(s.t, resp.Body)
					if asset.path != "/swagger/" {
						return
					}
					html := string(resp.Body)
					require.Contains(s.t, html, "SwaggerUIBundle")
					require.Contains(s.t, html, "/openapi.yaml")
					require.Regexp(s.t, `validatorUrl\s*:\s*null`, html)
					require.Regexp(s.t, `queryConfigEnabled\s*:\s*false`, html)
					var css, bundle bool
					for _, match := range regexp.MustCompile(`(?:src|href)=["']([^"']+)["']`).FindAllStringSubmatch(html, -1) {
						assetURL := match[1]
						parsed, err := url.Parse(assetURL)
						require.NoError(s.t, err)
						require.Empty(s.t, parsed.Scheme, "Swagger assets must stay local")
						require.False(s.t, strings.HasPrefix(assetURL, "//"), "Swagger assets must stay local")
						css = css || strings.HasSuffix(assetURL, "swagger-ui.css")
						bundle = bundle || strings.HasSuffix(assetURL, "swagger-ui-bundle.js")
					}
					require.True(s.t, css, "Swagger must load the bundled stylesheet")
					require.True(s.t, bundle, "Swagger must load the bundled JavaScript")
				})
			})
		}
	}
	runCase(t, "PUB-023", "public", "Swagger rejects POST", func(s *scenario) {
		s.step("Return 405 and advertise GET", func() {
			resp := s.raw("POST", "/swagger/", nil, "", 405, "METHOD_NOT_ALLOWED")
			methods := strings.Split(resp.Header.Get("Allow"), ",")
			for i := range methods {
				methods[i] = strings.TrimSpace(methods[i])
			}
			require.Contains(s.t, methods, "GET")
		})
	})
	runCase(t, "PUB-024", "public", "An existing protected route still requires authentication", func(s *scenario) {
		s.step("Authenticate before resolving a product", func() {
			s.request("GET", "/products/1", nil, "", 401, "TOKEN_INVALID")
		})
	})
}
