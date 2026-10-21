package server

import (
	"encoding/json"
	"github.com/golang-jwt/jwt/v5"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"golang.org/x/crypto/bcrypt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestJWTRejectsWrongTypeSignatureAndExpiry(t *testing.T) {
	s := &Server{JWTSecret: []byte("test-secret-at-least-32-characters")}
	token, err := s.signToken(principal{ID: 42, Role: "SELLER"}, "access", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.parseToken(token, "access")
	if err != nil || p.ID != 42 || p.Role != "SELLER" {
		t.Fatalf("valid token: %+v %v", p, err)
	}
	if _, err = s.parseToken(token, "refresh"); err == nil {
		t.Fatal("access accepted as refresh")
	}
	other := &Server{JWTSecret: []byte("different-secret")}
	if _, err = other.parseToken(token, "access"); err == nil {
		t.Fatal("invalid signature accepted")
	}
	expired, _ := s.signToken(p, "access", time.Now().Add(-time.Minute))
	if _, err = s.parseToken(expired, "access"); err == nil {
		t.Fatal("expired token accepted")
	}
}

func TestJWTRequiresIssuerExpiryAndHS256(t *testing.T) {
	s := &Server{JWTSecret: []byte("test-secret")}
	for _, claims := range []jwt.MapClaims{
		{"sub": "1", "role": "USER", "type": "access", "iss": "soa-marketplace"},
		{"sub": "1", "role": "USER", "type": "access", "iss": "other", "exp": time.Now().Add(time.Minute).Unix()},
		{"sub": "0", "role": "USER", "type": "access", "iss": "soa-marketplace", "exp": time.Now().Add(time.Minute).Unix()},
	} {
		raw, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.JWTSecret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.parseToken(raw, "access"); err == nil {
			t.Fatalf("accepted invalid claims: %v", claims)
		}
	}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodHS384, jwt.MapClaims{"sub": "1", "role": "USER", "type": "access", "iss": "soa-marketplace", "exp": time.Now().Add(time.Minute).Unix()}).SignedString(s.JWTSecret)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.parseToken(raw, "access"); err == nil {
		t.Fatal("accepted HS384")
	}
}

func TestRegistrationByteLimitDoesNotRejectValidRole(t *testing.T) {
	body := `{"email":"user@example.test","password":"` + strings.Repeat("я", 40) + `","role":"USER"}`
	recorder := httptest.NewRecorder()
	(&Server{}).Register(recorder, httptest.NewRequest("POST", "/auth/register", strings.NewReader(body)))
	if recorder.Code != 400 {
		t.Fatalf("status=%d", recorder.Code)
	}
	var response api.Error
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Details == nil || (*response.Details)["password"] == nil {
		t.Fatal("missing password violation")
	}
	if (*response.Details)["role"] != nil {
		t.Fatal("valid role rejected")
	}
}

func TestExistingBootstrapCredentials(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("original-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role      api.Role
		password  string
		wantError bool
	}{
		{api.RoleADMIN, "original-password", false},
		{api.RoleADMIN, "changed-password", true},
		{api.RoleUSER, "original-password", true},
	} {
		err := checkBootstrapCredentials(tc.role, string(hash), tc.password)
		if (err != nil) != tc.wantError {
			t.Fatalf("role=%s error=%v", tc.role, err)
		}
		if err != nil && (strings.Contains(err.Error(), tc.password) || strings.Contains(err.Error(), string(hash))) {
			t.Fatal("credentials exposed")
		}
	}
}
