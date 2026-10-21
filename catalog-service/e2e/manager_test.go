package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	allure "github.com/allure-framework/allure-go/commons/gotest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const fixturePassword = "Go-e2e-password-123"

type requestRecord struct{ Method, Path string }
type testRun struct {
	id, root, dir, docker string
	cfg                   config
	err                   error
	http                  *http.Client
	fixtures              map[string][]int64
	requests              map[string]requestRecord
	secrets               []string
	adminToken            string
}
type scenario struct {
	t                     *testing.T
	a                     *allure.Context
	run                   *testRun
	sellerUser, buyerUser *user
}

var once sync.Once
var shared *testRun

func getRun() *testRun {
	once.Do(func() {
		cwd, _ := os.Getwd()
		root := filepath.Clean(filepath.Join(cwd, "../.."))
		r := &testRun{id: strings.ToUpper(strings.ReplaceAll(uuid.NewString(), "-", "")[:10]), root: root,
			docker: os.Getenv("DOCKER_CONTEXT"), fixtures: map[string][]int64{"users": {}, "products": {}, "orders": {}, "promos": {}},
			requests: map[string]requestRecord{}, secrets: []string{fixturePassword},
			http: &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
		shared = r
		if r.docker == "" {
			r.docker = "colima-distsys"
		}
		base := os.Getenv("E2E_ARTIFACTS_DIR")
		if base == "" {
			base = filepath.Join(root, "artifacts", "e2e")
		}
		base, _ = filepath.Abs(base)
		r.dir = filepath.Join(base, r.id)
		if err := os.MkdirAll(filepath.Join(r.dir, "allure-results"), 0755); err != nil {
			r.err = errors.New("cannot create run artifacts")
			return
		}
		if err := os.WriteFile(filepath.Join(base, "latest-run"), []byte(r.id+"\n"), 0644); err != nil {
			r.err = err
			return
		}
		if err := os.Setenv("ALLURE_RESULTS_DIR", filepath.Join(r.dir, "allure-results")); err != nil {
			r.err = err
			return
		}
		data, err := r.command("config", "--format", "json")
		if err != nil {
			r.err = errors.New("cannot resolve Compose; check Docker context and configuration")
			return
		}
		r.cfg, r.err = resolveConfig([]byte(data), map[string]string{"BASE_URL": os.Getenv("BASE_URL")})
		r.secrets = append(r.secrets, r.cfg.AdminPassword)
	})
	return shared
}

func runCase(t *testing.T, id, group, name string, fn func(*scenario)) {
	t.Helper()
	r := getRun()
	allure.Test(t, id+"_"+name, func(a *allure.Context) {
		s := &scenario{t: a.T(), a: a, run: r}
		defer func() {
			if s.t.Failed() && r.cfg.BaseURL != "" {
				s.failureEvidence()
			}
			require.NoError(s.t, r.save(), "cannot save fixture manifest")
			manifest, _ := os.ReadFile(filepath.Join(r.dir, "fixtures.json"))
			a.Attachment("Fixtures", manifest, "application/json")
		}()
		require.NoError(s.t, r.err)
		fn(s)
	}, allure.WithTestCaseID(id), allure.WithFeature(group), allure.WithStory(name),
		allure.WithSuite("Marketplace API"), allure.WithDescription(name),
		allure.WithLink("https://github.com/4e6yPeK48/soa-marketplace/blob/main/docs/qa.md", "QA strategy", "link"))
}
func (s *scenario) step(name string, fn func()) {
	s.a.Step(name, func(a *allure.Context) { old := s.a; s.a = a; defer func() { s.a = old }(); fn() })
}
func (s *scenario) precondition(name string, fn func()) { s.step("Подготовка: "+name, fn) }

func (r *testRun) command(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", append([]string{"--context", r.docker, "compose"}, args...)...)
	cmd.Dir = r.root
	out, err := cmd.Output()
	if err != nil {
		return "", errors.New("Docker Compose command failed")
	}
	return string(out), nil
}
func (r *testRun) save() error {
	metadata := M{"run": r.id, "go": runtime.Version(), "os": runtime.GOOS, "docker_context": r.docker, "base_url": r.cfg.BaseURL, "cooldown_minutes": r.cfg.CooldownMinutes}
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = r.root
	if data, err := cmd.Output(); err == nil {
		metadata["commit"] = strings.TrimSpace(string(data))
	}
	dirty := exec.Command("git", "status", "--porcelain")
	dirty.Dir = r.root
	if data, err := dirty.Output(); err == nil {
		metadata["dirty"] = len(data) > 0
	}
	for name, value := range map[string]any{"run.json": metadata, "fixtures.json": M{"run": r.id, "ids": r.fixtures}} {
		data, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return err
		}
		if err = os.WriteFile(filepath.Join(r.dir, name), data, 0644); err != nil {
			return err
		}
	}
	return nil
}
func (r *testRun) redact(text string) string {
	for _, secret := range r.secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[REDACTED]")
		}
	}
	return text
}
func (r *testRun) collectSecrets(body []byte) {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return
	}
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for key, child := range v {
				if str, ok := child.(string); ok && (key == "access_token" || key == "refresh_token") {
					if str != "" && !slices.Contains(r.secrets, str) {
						r.secrets = append(r.secrets, str)
					}
				} else {
					visit(child)
				}
			}
		case []any:
			for _, child := range v {
				visit(child)
			}
		}
	}
	visit(value)
}
func (s *scenario) observe(method, path string, body []byte, token string, r response, err error) {
	s.t.Helper()
	if r.Status >= 200 && r.Status < 300 {
		s.run.collectSecrets(r.Body)
	}
	request := M{"method": method, "path": path, "body": string(sanitizeJSON(body))}
	if token != "" {
		request["authorization"] = "[REDACTED]"
	}
	data, _ := json.MarshalIndent(request, "", "  ")
	s.a.Attachment("HTTP request", data, "application/json")
	require.NoError(s.t, err)
	headers := r.Header.Clone()
	for key := range headers {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "cookie") || strings.Contains(lower, "authorization") {
			headers.Set(key, "[REDACTED]")
		}
	}
	data, _ = json.MarshalIndent(M{"status": r.Status, "headers": headers}, "", "  ")
	s.a.Attachment("HTTP response", []byte(s.run.redact(string(data))), "application/json")
	if strings.Contains(r.Header.Get("Content-Type"), "json") {
		s.a.Attachment("Response body", []byte(s.run.redact(string(sanitizeJSON(r.Body)))), "application/json")
	} else {
		text := s.run.redact(string(r.Body))
		if len(text) > 32768 {
			text = text[:32768] + "\n[truncated]"
		}
		s.a.Attachment("Response body", []byte(text), "text/plain")
	}
	id := r.Header.Get("X-Request-Id")
	parsed, parseErr := uuid.Parse(id)
	require.NoError(s.t, parseErr, "missing/invalid request UUID")
	require.Equal(s.t, parsed.String(), strings.ToLower(id), "request UUID must be canonical")
	s.run.requests[id] = requestRecord{Method: method, Path: path}
}
func (s *scenario) raw(method, path string, body []byte, token string, status int, code string) response {
	s.t.Helper()
	var result response
	s.step(method+" "+path, func() {
		var err error
		result, err = s.exchange(method, path, body, token)
		s.observe(method, path, body, token, result, err)
		require.Equal(s.t, status, result.Status, "%s %s: %s", method, path, s.run.redact(string(sanitizeJSON(result.Body))))
		if code != "" {
			require.NotNil(s.t, result.JSON, "expected JSON error")
			require.Equal(s.t, code, result.JSON["error_code"])
			require.NotEmpty(s.t, result.JSON["message"])
			if code == "ORDER_LIMIT_EXCEEDED" {
				n := integer(object(result.JSON["details"])["retry_after_seconds"])
				require.GreaterOrEqual(s.t, n, int64(1))
				require.Equal(s.t, strconv.FormatInt(n, 10), result.Header.Get("Retry-After"))
			}
			if code == "INVALID_STATE_TRANSITION" {
				d := object(result.JSON["details"])
				for _, key := range []string{"current_status", "requested_status", "allowed_statuses"} {
					require.Contains(s.t, d, key)
				}
			}
		}
	})
	return result
}
func (s *scenario) request(method, path string, body any, token string, status int, code string) M {
	s.t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		require.NoError(s.t, err)
	}
	result := s.raw(method, path, data, token, status, code)
	if len(result.Body) > 0 {
		require.NotNil(s.t, result.JSON, "expected a JSON object response")
	}
	return result.JSON
}
func (s *scenario) user(role string) user {
	email := fmt.Sprintf("go-e2e-%s-%d@example.test", s.run.id, len(s.run.fixtures["users"]))
	out := s.request("POST", "/auth/register", M{"email": email, "password": fixturePassword, "role": role}, "", 201, "")
	id := integer(out["id"])
	s.run.fixtures["users"] = append(s.run.fixtures["users"], id)
	pair := s.request("POST", "/auth/login", M{"email": email, "password": fixturePassword}, "", 200, "")
	return user{ID: id, Email: email, Token: pair["access_token"].(string), Pair: pair}
}
func (s *scenario) seller() user {
	if s.sellerUser == nil {
		u := s.user("SELLER")
		s.sellerUser = &u
	}
	return *s.sellerUser
}
func (s *scenario) buyer() user {
	if s.buyerUser == nil {
		u := s.user("USER")
		s.buyerUser = &u
	}
	return *s.buyerUser
}
func (s *scenario) admin() string {
	if s.run.adminToken == "" {
		out := s.request("POST", "/auth/login", M{"email": s.run.cfg.AdminEmail, "password": s.run.cfg.AdminPassword}, "", 200, "")
		s.run.adminToken = out["access_token"].(string)
	}
	return s.run.adminToken
}
func merge(base, changes M) M {
	for k, v := range changes {
		base[k] = v
	}
	return base
}
func (s *scenario) product(token string, changes M) M {
	body := merge(M{"name": fmt.Sprintf("Go-E2E-%s-%d", s.run.id, len(s.run.fixtures["products"])), "description": nil, "price": 100, "stock": 20, "category": s.run.id, "status": "ACTIVE"}, changes)
	out := s.request("POST", "/products", body, token, 201, "")
	s.run.fixtures["products"] = append(s.run.fixtures["products"], integer(out["id"]))
	return out
}
func (s *scenario) replaceProduct(p M, changes M) M {
	body := M{}
	for _, key := range []string{"name", "description", "price", "stock", "category", "status"} {
		if v, ok := p[key]; ok {
			body[key] = v
		}
	}
	return s.request("PUT", pathID("/products", integer(p["id"])), merge(body, changes), s.seller().Token, 200, "")
}
func (s *scenario) promo(changes M) M {
	now := time.Now().UTC()
	body := merge(M{"code": fmt.Sprintf("P%s%d", s.run.id, len(s.run.fixtures["promos"])), "discount_type": "PERCENTAGE", "discount_value": 20, "min_order_amount": 0, "max_uses": 10, "valid_from": now.Add(-24 * time.Hour).Format(time.RFC3339Nano), "valid_until": now.Add(24 * time.Hour).Format(time.RFC3339Nano), "active": true}, changes)
	out := s.request("POST", "/promo-codes", body, s.seller().Token, 201, "")
	s.run.fixtures["promos"] = append(s.run.fixtures["promos"], integer(out["id"]))
	return out
}
func (s *scenario) order(u user, p M, qty int, promo M) M {
	body := items(p, qty)
	if promo != nil {
		body["promo_code"] = promo["code"]
	}
	out := s.request("POST", "/orders", body, u.Token, 201, "")
	s.rememberOrder(integer(out["id"]))
	return out
}
func (s *scenario) rememberOrder(id int64) {
	s.run.fixtures["orders"] = append(s.run.fixtures["orders"], id)
}
func (s *scenario) cancel(u user, o M) M {
	return s.request("POST", pathID("/orders", integer(o["id"]))+"/cancel", nil, u.Token, 200, "")
}
func (s *scenario) stock(p M) int64 {
	n, err := strconv.ParseInt(s.sql(fmt.Sprintf("SELECT stock FROM products WHERE id=%d", integer(p["id"]))), 10, 64)
	require.NoError(s.t, err)
	return n
}
func (s *scenario) uses(p M) int64 {
	n, err := strconv.ParseInt(s.sql(fmt.Sprintf("SELECT current_uses FROM promo_codes WHERE id=%d", integer(p["id"]))), 10, 64)
	require.NoError(s.t, err)
	return n
}
func (s *scenario) age(u user) {
	q, err := ageQuery(u.ID, s.run.cfg.CooldownMinutes, s.run.fixtures["users"])
	require.NoError(s.t, err)
	s.sql(q)
}
func (s *scenario) sql(query string) string {
	require.NotContains(s.t, strings.ToLower(query), "password")
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "UPDATE") {
		var table string
		var id int64
		for _, pair := range []struct{ prefix, field, key string }{{"UPDATE user_operations", "user_id=", "users"}, {"UPDATE promo_codes", "id=", "promos"}} {
			if strings.HasPrefix(query, pair.prefix) {
				parts := strings.Split(query, "WHERE "+pair.field)
				require.Len(s.t, parts, 2)
				var err error
				id, err = strconv.ParseInt(parts[1], 10, 64)
				require.NoError(s.t, err)
				require.Contains(s.t, s.run.fixtures[pair.key], id)
				table = pair.key
			}
		}
		require.NotEmpty(s.t, table, "unscoped SQL mutation refused")
	} else {
		require.True(s.t, strings.HasPrefix(strings.ToUpper(strings.TrimSpace(query)), "SELECT"), "only scoped fixture updates and SELECT allowed")
	}
	out, err := s.run.command("exec", "-T", "postgres", "psql", "-U", "marketplace", "-d", "marketplace", "-At", "-v", "ON_ERROR_STOP=1", "-c", query)
	require.NoError(s.t, err)
	out = strings.TrimSpace(out)
	s.a.Attachment("SQL", []byte(s.run.redact(query+"\n"+out)), "text/plain")
	return out
}
func (s *scenario) snapshot(u user, p, o M) []string {
	id := integer(o["id"])
	return []string{strconv.FormatInt(s.stock(p), 10), s.sql(fmt.Sprintf("SELECT row_to_json(o) FROM orders o WHERE id=%d", id)), s.sql(fmt.Sprintf("SELECT row_to_json(i) FROM order_items i WHERE order_id=%d ORDER BY id", id)), s.sql(fmt.Sprintf("SELECT count(*) FROM user_operations WHERE user_id=%d", u.ID))}
}
