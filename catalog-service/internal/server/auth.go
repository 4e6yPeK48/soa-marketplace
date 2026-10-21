package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"golang.org/x/crypto/bcrypt"
)

type tokenClaims struct {
	jwt.RegisteredClaims
	Role api.Role `json:"role"`
	Type string   `json:"type"`
}

func (s *Server) signToken(p principal, kind string, expiry time.Time) (string, error) {
	claims := tokenClaims{RegisteredClaims: jwt.RegisteredClaims{Subject: strconv.FormatInt(p.ID, 10), Issuer: "soa-marketplace", ExpiresAt: jwt.NewNumericDate(expiry)}, Role: p.Role, Type: kind}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.JWTSecret)
}

func (s *Server) parseToken(raw, kind string) (principal, error) {
	invalid := &apiError{Status: 401, Code: "TOKEN_INVALID", Message: "Invalid access token"}
	if kind == "refresh" {
		invalid.Code = "REFRESH_TOKEN_INVALID"
		invalid.Message = "Invalid refresh token"
	}
	claims := new(tokenClaims)
	_, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) { return s.JWTSecret, nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("soa-marketplace"), jwt.WithExpirationRequired())
	if err != nil {
		if kind == "access" && errors.Is(err, jwt.ErrTokenExpired) {
			invalid.Code = "TOKEN_EXPIRED"
			invalid.Message = "Access token expired"
		}
		return principal{}, invalid
	}
	id, err := strconv.ParseInt(claims.Subject, 10, 64)
	if err != nil || id <= 0 || !claims.Role.Valid() || claims.Type != kind {
		return principal{}, invalid
	}
	return principal{ID: id, Role: claims.Role}, nil
}

func (s *Server) Authenticate(r *http.Request) (principal, error) {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return principal{}, &apiError{Status: 401, Code: "TOKEN_INVALID", Message: "Bearer access token required"}
	}
	return s.parseToken(parts[1], "access")
}

func (s *Server) BootstrapAdmin(ctx context.Context, email, password string) error {
	if len(password) > 72 {
		return errors.New("admin password exceeds bcrypt 72-byte limit")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	email = strings.ToLower(strings.TrimSpace(email))
	result, err := s.DB.ExecContext(ctx, `INSERT INTO users(email,password_hash,role) VALUES($1,$2,'ADMIN') ON CONFLICT(email) DO NOTHING`, email, string(hash))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted > 0 {
		return err
	}
	var role api.Role
	var existingHash string
	if err := s.DB.QueryRowContext(ctx, `SELECT role,password_hash FROM users WHERE email=$1`, email).Scan(&role, &existingHash); err != nil {
		return err
	}
	return checkBootstrapCredentials(role, existingHash, password)
}

func checkBootstrapCredentials(role api.Role, hash, password string) error {
	if role != api.RoleADMIN {
		return errors.New("ADMIN_EMAIL belongs to an existing non-admin account; choose another ADMIN_EMAIL")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return errors.New("ADMIN_PASSWORD does not match the existing administrator; use the original password or choose another ADMIN_EMAIL")
	}
	return nil
}

func (s *Server) Register(w http.ResponseWriter, r *http.Request) {
	var input api.RegisterRequest
	if !decodeBody(w, r, &input) {
		return
	}
	violations := map[string]any{}
	if !input.Role.Valid() {
		violations["role"] = "USER or SELLER required"
	}
	if len(input.Password) > 72 {
		violations["password"] = "Maximum 72 bytes"
	}
	if len(violations) > 0 {
		writeError(w, 400, "VALIDATION_ERROR", "Invalid registration", violations)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), bcrypt.DefaultCost)
	if err != nil {
		s.respondError(w, err)
		return
	}
	user := api.UserResponse{Email: input.Email, Role: api.Role(input.Role)}
	email := strings.ToLower(strings.TrimSpace(string(input.Email)))
	err = s.DB.QueryRowContext(r.Context(), `INSERT INTO users(email,password_hash,role) VALUES($1,$2,$3) RETURNING id,email`, email, string(hash), user.Role).Scan(&user.Id, &user.Email)
	var pgerr *pgconn.PgError
	if errors.As(err, &pgerr) && pgerr.Code == "23505" {
		writeError(w, 409, "EMAIL_ALREADY_EXISTS", "Email is already registered", nil)
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 201, user)
}

func (s *Server) Login(w http.ResponseWriter, r *http.Request) {
	var input api.LoginRequest
	if !decodeBody(w, r, &input) {
		return
	}
	var p principal
	var hash string
	err := s.DB.QueryRowContext(r.Context(), `SELECT id,role,password_hash FROM users WHERE email=$1`, strings.ToLower(strings.TrimSpace(string(input.Email)))).Scan(&p.ID, &p.Role, &hash)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) != nil) {
		writeError(w, 401, "TOKEN_INVALID", "Invalid email or password", nil)
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.issueTokens(w, p)
}

func (s *Server) Refresh(w http.ResponseWriter, r *http.Request) {
	var input api.RefreshRequest
	if !decodeBody(w, r, &input) {
		return
	}
	p, err := s.parseToken(input.RefreshToken, "refresh")
	if err != nil {
		s.respondError(w, err)
		return
	}
	err = s.DB.QueryRowContext(r.Context(), `SELECT role FROM users WHERE id=$1`, p.ID).Scan(&p.Role)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 401, "REFRESH_TOKEN_INVALID", "Refresh token user no longer exists", nil)
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	s.issueTokens(w, p)
}

func (s *Server) issueTokens(w http.ResponseWriter, p principal) {
	now := time.Now()
	access, err := s.signToken(p, "access", now.Add(20*time.Minute))
	if err != nil {
		s.respondError(w, err)
		return
	}
	refresh, err := s.signToken(p, "refresh", now.Add(14*24*time.Hour))
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 200, api.TokenPair{AccessToken: access, RefreshToken: refresh, TokenType: api.Bearer, ExpiresIn: 1200})
}
