package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
)

func TestRequestValidationAndSafeLogs(t *testing.T) {
	for _, body := range []string{
		`{"email":"not-an-email","password":"supersecret-password"}`,
		`{"email":"person@example.test","password":""}`,
		`{"email":"person@example.test","password":"supersecret-password"`,
		`{"email":"person@example.test","password":"supersecret-password"}{}`,
	} {
		t.Run(body, func(t *testing.T) {
			var logs bytes.Buffer
			s := &Server{JWTSecret: []byte(strings.Repeat("x", 32)), Logger: slog.New(slog.NewJSONHandler(&logs, nil))}
			h, err := New(s)
			if err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("POST", "/auth/login", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 400 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			var response map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response["error_code"] != "VALIDATION_ERROR" || response["details"] == nil {
				t.Fatal(response)
			}
			if _, err := uuid.Parse(w.Header().Get("X-Request-Id")); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(logs.String(), "supersecret-password") {
				t.Fatal("password leaked in request log")
			}
			var event map[string]any
			if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"request_id", "method", "endpoint", "status_code", "duration_ms", "user_id", "timestamp"} {
				if _, ok := event[key]; !ok {
					t.Fatalf("missing log field %s", key)
				}
			}
		})
	}
}

func TestExactMoneyBoundary(t *testing.T) {
	for _, tc := range []struct {
		price string
		valid bool
	}{
		{"0.01", true}, {"0.29", true}, {"1.2300", true}, {"9999999999.99", true},
		{"1.001", false}, {"0.009", false}, {"9999999999.99000000001", false},
		{"1e-2147483647", false},
	} {
		t.Run(tc.price, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/products", strings.NewReader(`{"price":`+tc.price+`}`))
			w := httptest.NewRecorder()
			var input api.ProductCreate
			if got := decodeBody(w, r, &input); got != tc.valid {
				t.Fatalf("valid=%v want=%v response=%s", got, tc.valid, w.Body)
			}
		})
	}
}

func TestProductsRequireAuthentication(t *testing.T) {
	h, err := New(&Server{JWTSecret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/products", nil))
	if w.Code != 401 || !strings.Contains(w.Body.String(), "TOKEN_INVALID") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

func TestRouteErrorsDoNotRequireAuthentication(t *testing.T) {
	h, err := New(&Server{JWTSecret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path string
		status       int
		code, allow  string
	}{
		{"GET", "/missing", 404, "ROUTE_NOT_FOUND", ""},
		{"POST", "/auth/missing", 404, "ROUTE_NOT_FOUND", ""},
		{"GET", "/products/1/missing", 404, "ROUTE_NOT_FOUND", ""},
		{"GET", "/health/missing", 404, "ROUTE_NOT_FOUND", ""},
		{"GET", "/swagger/missing.js", 404, "ROUTE_NOT_FOUND", ""},
		{"GET", "/swagger/missing", 404, "ROUTE_NOT_FOUND", ""},
		{"POST", "/swagger/", 405, "METHOD_NOT_ALLOWED", "GET"},
		{"PATCH", "/products", 405, "METHOD_NOT_ALLOWED", "POST"},
		{"GET", "/auth/login", 405, "METHOD_NOT_ALLOWED", "POST"},
		{"POST", "/", 405, "METHOD_NOT_ALLOWED", "GET"},
		{"POST", "/health", 405, "METHOD_NOT_ALLOWED", "GET"},
		{"POST", "/openapi.yaml", 405, "METHOD_NOT_ALLOWED", "GET"},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			for _, token := range []string{"", "Bearer invalid-token"} {
				r := httptest.NewRequest(tc.method, tc.path, nil)
				r.Header.Set("Authorization", token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != tc.status {
					t.Fatalf("status=%d body=%s", w.Code, w.Body)
				}
				var response api.Error
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if string(response.ErrorCode) != tc.code || !response.ErrorCode.Valid() {
					t.Fatal(response)
				}
				if tc.allow != "" && !strings.Contains(w.Header().Get("Allow"), tc.allow) {
					t.Fatal(w.Header())
				}
				if w.Header().Get("X-Request-Id") == "" {
					t.Fatal("missing request ID")
				}
			}
		})
	}
}

func TestPublicPages(t *testing.T) {
	h, err := New(&Server{JWTSecret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, contentType, content string }{
		{"/", "text/html", "Marketplace API"},
		{"/health", "text/plain", "ok\n"},
		{"/openapi.yaml", "application/yaml", "openapi:"},
		{"/swagger/", "text/html", "SwaggerUIBundle"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+tc.path, func(t *testing.T) {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, tc.path, nil))
				if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), tc.contentType) {
					t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
				}
				if method == http.MethodHead && w.Body.Len() != 0 {
					t.Fatal("HEAD contains body")
				}
				if method == http.MethodGet && !strings.Contains(w.Body.String(), tc.content) {
					t.Fatal(w.Body)
				}
				if tc.path == "/" && method == http.MethodGet {
					for _, link := range []string{`href="/health"`, `href="/openapi.yaml"`, `href="/swagger/"`} {
						if !strings.Contains(w.Body.String(), link) {
							t.Fatal("missing link", link)
						}
					}
				}
			})
		}
	}
}

func TestSwaggerAssetsAndSettings(t *testing.T) {
	h, err := New(&Server{JWTSecret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, contentType string }{
		{"/swagger/swagger-ui.css", "text/css"},
		{"/swagger/swagger-ui-bundle.js", "javascript"},
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", tc.path, nil)
		r.Header.Set("Authorization", "Bearer invalid-token")
		h.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Type"), tc.contentType) || w.Body.Len() < 1000 {
			t.Fatalf("%s: %d %v size=%d", tc.path, w.Code, w.Header(), w.Body.Len())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/swagger", nil))
	if (w.Code != 301 && w.Code != 308) || w.Header().Get("Location") != "/swagger/" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/swagger/", nil))
	for _, setting := range []string{`url: "/openapi.yaml"`, `validatorUrl: null`, `persistAuthorization: false`, `queryConfigEnabled: false`} {
		if !strings.Contains(w.Body.String(), setting) {
			t.Fatal("missing Swagger setting", setting)
		}
	}
}

func TestRedactionRecursesAndDropsMalformedBody(t *testing.T) {
	value := safeBody([]byte(`{"password":"secret","nested":{"refresh_token":"refresh-secret"},"list":[{"access_token":"access-secret"}]}`))
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatal(string(encoded))
	}
	if value := safeBody([]byte(`{"password":"secret"`)); value != "[invalid JSON]" {
		t.Fatal(value)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).respondError(w, &apiError{Status: 429, Code: "ORDER_LIMIT_EXCEEDED", Message: "Please wait", Details: map[string]any{"retry_after_seconds": 17}})
	if w.Code != 429 || w.Header().Get("Retry-After") != "17" {
		t.Fatalf("%d %v", w.Code, w.Header())
	}
}
