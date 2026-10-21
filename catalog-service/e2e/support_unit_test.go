package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConfiguration(t *testing.T) {
	data := []byte("{\"services\":{\"catalog\":{\"ports\":[{\"target\":8080,\"published\":\"9088\"}],\"environment\":{\"ADMIN_EMAIL\":\"custom@example.test\",\"ADMIN_PASSWORD\":\"custom-secret\",\"ORDER_COOLDOWN_MINUTES\":\"7\"}}}}")
	cfg, err := resolveConfig(data, nil)
	if err != nil || cfg.BaseURL != "http://127.0.0.1:9088" || cfg.AdminEmail != "custom@example.test" || cfg.AdminPassword != "custom-secret" || cfg.CooldownMinutes != 7 {
		t.Fatal("resolved Compose settings were not preserved")
	}
	data = []byte("{\"services\":{\"catalog\":{\"ports\":[{\"target\":8080,\"published\":\"9999\"}]}}}")
	cfg, err = resolveConfig(data, map[string]string{"BASE_URL": "http://example.test:7777/"})
	if err != nil || cfg.BaseURL != "http://example.test:7777" || cfg.AdminEmail != "admin@example.test" || cfg.AdminPassword != "admin-local-password" || cfg.CooldownMinutes != 1 {
		t.Fatal("override or defaults were not preserved")
	}
	if _, err := resolveConfig([]byte("{}"), nil); err == nil {
		t.Fatal("missing catalog configuration accepted")
	}
}

func TestAgeOnlyOwnFixtures(t *testing.T) {
	query, err := ageQuery(42, 7, []int64{42})
	if err != nil || !strings.Contains(query, "interval '8 minutes'") || !strings.Contains(query, "WHERE user_id=42") {
		t.Fatal("configured cooldown or owner scope lost")
	}
	if _, err := ageQuery(999, 7, []int64{42}); err == nil {
		t.Fatal("foreign fixture accepted")
	}
}

func TestExactJSONNumbers(t *testing.T) {
	value, err := decodeObject([]byte("{\"id\":9007199254740993,\"price\":0.29}"))
	if err != nil || integer(value["id"]) != 9007199254740993 || amount(value["price"]).String() != "0.29" {
		t.Fatal("JSON number lost precision")
	}
	wire, err := json.Marshal(M{"price": json.Number("0.29")})
	if err != nil || string(wire) != "{\"price\":0.29}" {
		t.Fatal("request money must be a JSON number")
	}
}

func TestRedactionBeforeAttachments(t *testing.T) {
	body := []byte("{\"Password\":\"secret-one\",\"nested\":[{\"access_token\":\"secret-two\"}],\"refresh_token\":\"secret-three\",\"name\":\"retained\"}")
	safe := string(sanitizeJSON(body))
	for _, secret := range []string{"secret-one", "secret-two", "secret-three"} {
		if strings.Contains(safe, secret) {
			t.Fatal("secret leaked")
		}
	}
	if !strings.Contains(safe, "retained") || strings.Contains(string(sanitizeJSON([]byte("{\"password\":\"incomplete-secret"))), "incomplete-secret") {
		t.Fatal("invalid JSON leaked or useful fields lost")
	}
}

func TestStrictJSONResponses(t *testing.T) {
	for _, body := range []string{`{"ok":true}garbage`, `{"ok":true}{}`, `null`} {
		if value, err := decodeObject([]byte(body)); err == nil || value != nil {
			t.Fatal("invalid JSON object accepted")
		}
	}
}

func TestMoneyRejectsNumericStrings(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("numeric string accepted as JSON money")
		}
	}()
	amount("0.29")
}

func TestKnownCredentialRedaction(t *testing.T) {
	r := &testRun{secrets: []string{"tiny"}}
	r.collectSecrets([]byte(`{"access_token":"issued-access","refresh_token":"issued-refresh","token_type":"Bearer","message":"malformed"}`))
	for _, secret := range []string{"tiny", "issued-access", "issued-refresh"} {
		if strings.Contains(r.redact("value="+secret), secret) {
			t.Fatal("known credential leaked")
		}
	}
	if r.redact("Bearer malformed") != "Bearer malformed" {
		t.Fatal("noncredential values registered as secrets")
	}
}

func TestHTTPResponseParsing(t *testing.T) {
	for _, tc := range []struct {
		body, contentType string
		invalid           bool
	}{
		{`{"ok":true}`, "application/json", false},
		{`{"ok":true}{}`, "application/json", true},
		{"<html>Swagger</html>", "text/html", false},
	} {
		t.Run(tc.contentType+tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			s := &scenario{t: t, run: &testRun{cfg: config{BaseURL: server.URL}, http: server.Client()}}
			result, err := s.exchange("GET", "/", nil, "")
			if (err != nil) != tc.invalid || string(result.Body) != tc.body || result.Status != 200 {
				t.Fatal("response parsing or diagnostic body lost")
			}
		})
	}
}
