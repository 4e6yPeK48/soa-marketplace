package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type M = map[string]any
type config struct {
	BaseURL, AdminEmail, AdminPassword string
	CooldownMinutes                    int
}
type response struct {
	Status int
	Header http.Header
	Body   []byte
	JSON   M
}
type user struct {
	ID           int64
	Token, Email string
	Pair         M
}

func resolveConfig(data []byte, env map[string]string) (config, error) {
	cfg := config{AdminEmail: "admin@example.test", AdminPassword: "admin-local-password", CooldownMinutes: 1}
	var doc struct {
		Services map[string]struct {
			Ports []struct {
				Target    int
				Published string
			}
			Environment map[string]string
		}
	}
	if json.Unmarshal(data, &doc) != nil {
		return cfg, errors.New("cannot decode Compose configuration")
	}
	catalog, ok := doc.Services["catalog"]
	if !ok {
		return cfg, errors.New("catalog service missing from Compose configuration")
	}
	for _, port := range catalog.Ports {
		if port.Target == 8080 {
			cfg.BaseURL = "http://127.0.0.1:" + port.Published
		}
	}
	if cfg.BaseURL == "" {
		return cfg, errors.New("catalog published port missing")
	}
	if value := catalog.Environment["ADMIN_EMAIL"]; value != "" {
		cfg.AdminEmail = value
	}
	if value := catalog.Environment["ADMIN_PASSWORD"]; value != "" {
		cfg.AdminPassword = value
	}
	if value := catalog.Environment["ORDER_COOLDOWN_MINUTES"]; value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > 1440 {
			return cfg, errors.New("invalid order cooldown")
		}
		cfg.CooldownMinutes = n
	}
	if value := env["BASE_URL"]; value != "" {
		cfg.BaseURL = strings.TrimRight(value, "/")
	}
	return cfg, nil
}

func decodeObject(body []byte) (M, error) {
	var out M
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := d.Decode(&out); err != nil || out == nil {
		return nil, errors.New("expected a JSON object")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, errors.New("unexpected trailing JSON")
	}
	return out, nil
}
func object(v any) M    { return v.(map[string]any) }
func array(v any) []any { return v.([]any) }
func integer(v any) int64 {
	switch n := v.(type) {
	case json.Number:
		out, err := n.Int64()
		if err != nil {
			panic("expected an exact JSON integer")
		}
		return out
	case int:
		return int64(n)
	case int64:
		return n
	default:
		panic(fmt.Sprintf("expected integer, got %T", v))
	}
}
func amount(v any) decimal.Decimal {
	number, ok := v.(json.Number)
	if !ok {
		panic("expected a JSON number")
	}
	out, err := decimal.NewFromString(string(number))
	if err != nil {
		panic("expected exact decimal")
	}
	return out
}
func pathID(prefix string, id int64) string { return prefix + "/" + strconv.FormatInt(id, 10) }
func items(p M, qty int) M                  { return M{"items": []M{{"product_id": integer(p["id"]), "quantity": qty}}} }
func ageQuery(id int64, minutes int, owned []int64) (string, error) {
	if !slices.Contains(owned, id) {
		return "", errors.New("refusing to age a foreign fixture")
	}
	return fmt.Sprintf("UPDATE user_operations SET created_at=NOW()-interval '%d minutes' WHERE user_id=%d", minutes+1, id), nil
}

func sanitizeJSON(body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	var value any
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if d.Decode(&value) != nil {
		return []byte("[invalid JSON]")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return []byte("[invalid JSON]")
	}
	var clean func(any)
	clean = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, child := range v {
				key := strings.ToLower(k)
				if strings.Contains(key, "password") || strings.Contains(key, "token") || strings.Contains(key, "secret") || strings.Contains(key, "authorization") || strings.Contains(key, "cookie") {
					v[k] = "[REDACTED]"
				} else {
					clean(child)
				}
			}
		case []any:
			for _, child := range v {
				clean(child)
			}
		}
	}
	clean(value)
	out, _ := json.MarshalIndent(value, "", "  ")
	return out
}

func (s *scenario) exchange(method, path string, body []byte, token string) (response, error) {
	req, err := http.NewRequestWithContext(s.t.Context(), method, s.run.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return response{}, errors.New("invalid HTTP request")
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	r, err := s.run.http.Do(req)
	if err != nil {
		return response{}, errors.New("cannot reach marketplace; run make up and check BASE_URL / PORT")
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	value, jsonErr := decodeObject(data)
	if err == nil && len(data) > 0 && strings.Contains(r.Header.Get("Content-Type"), "json") && jsonErr != nil {
		err = errors.New("response is not a valid JSON object")
	}
	return response{Status: r.StatusCode, Header: r.Header, Body: data, JSON: value}, err
}
