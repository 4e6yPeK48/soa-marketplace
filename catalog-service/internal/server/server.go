package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/google/uuid"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	middleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/shopspring/decimal"
)

func init() { decimal.MarshalJSONWithoutQuotes = true }

type Server struct {
	DB            *sql.DB
	JWTSecret     []byte
	OrderCooldown time.Duration
	Logger        *slog.Logger
}

type principal struct {
	ID   int64
	Role api.Role
}
type principalKey struct{}

func currentUser(r *http.Request) principal {
	if p, ok := r.Context().Value(principalKey{}).(*principal); ok {
		return *p
	}
	return principal{}
}

type apiError struct {
	Status        int
	Code, Message string
	Details       map[string]any
}

func (e *apiError) Error() string { return e.Message }

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body != nil {
		_ = json.NewEncoder(w).Encode(body)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string, details map[string]any) {
	response := api.Error{ErrorCode: api.ErrorErrorCode(code), Message: message}
	if details != nil {
		response.Details = &details
	}
	writeJSON(w, status, response)
}

func (s *Server) respondError(w http.ResponseWriter, err error) {
	var problem *apiError
	if errors.As(err, &problem) {
		if seconds, ok := problem.Details["retry_after_seconds"].(int); problem.Code == "ORDER_LIMIT_EXCEEDED" && ok {
			w.Header().Set("Retry-After", strconv.Itoa(seconds))
		}
		writeError(w, problem.Status, problem.Code, problem.Message, problem.Details)
		return
	}
	writeError(w, 500, "INTERNAL_ERROR", "Internal server error", nil)
}

func decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(target); err != nil {
		writeError(w, 400, "VALIDATION_ERROR", "Invalid JSON body", map[string]any{"body": "Expected a valid JSON object"})
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		writeError(w, 400, "VALIDATION_ERROR", "Invalid JSON body", map[string]any{"body": "Expected exactly one JSON object"})
		return false
	}
	return validateMoney(w, target)
}

func (s *Server) allowed(w http.ResponseWriter, r *http.Request, roles ...api.Role) bool {
	for _, role := range roles {
		if currentUser(r).Role == role {
			return true
		}
	}
	writeError(w, 403, "ACCESS_DENIED", "Operation is not allowed for this role", nil)
	return false
}

func New(s *Server) (http.Handler, error) {
	if s.Logger == nil {
		s.Logger = slog.Default()
	}
	spec, err := api.GetSwagger()
	if err != nil {
		return nil, err
	}
	if err = spec.Validate(context.Background()); err != nil {
		return nil, err
	}
	spec.Servers = nil
	// kin-openapi uses float64 for multipleOf; decoded money is checked exactly before business logic.
	for _, schema := range spec.Components.Schemas {
		for _, property := range schema.Value.Properties {
			if property.Value.Format == "decimal" {
				property.Value.MultipleOf = nil
			}
		}
	}
	mux := http.NewServeMux()
	apiHandler := api.HandlerWithOptions(s, api.StdHTTPServerOptions{
		BaseRouter: mux,
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeError(w, 400, "VALIDATION_ERROR", "Invalid request parameter", map[string]any{"parameter": err.Error()})
		},
	})
	mux.HandleFunc("GET /{$}", home)
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /openapi.yaml", specification)
	assets, err := fs.Sub(swaggerFiles, "swagger")
	if err != nil {
		return nil, err
	}
	swagger := http.StripPrefix("/swagger/", http.FileServerFS(assets))
	mux.Handle("GET /swagger", http.RedirectHandler("/swagger/", http.StatusPermanentRedirect))
	for _, path := range []string{"/swagger/{$}", "/swagger/swagger-ui.css", "/swagger/swagger-ui-bundle.js"} {
		mux.Handle("GET "+path, swagger)
	}
	validated := middleware.OapiRequestValidatorWithOptions(spec, &middleware.Options{
		Options: openapi3filter.Options{MultiError: true, AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		ErrorHandlerWithOpts: func(_ context.Context, err error, w http.ResponseWriter, _ *http.Request, _ middleware.ErrorHandlerOpts) {
			writeError(w, 400, "VALIDATION_ERROR", "Request does not match the OpenAPI contract", validationDetails(err))
		},
	})(apiHandler)
	protected := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w = headWriter{w}
		}
		handler, pattern := mux.Handler(r)
		if pattern == "" {
			handler.ServeHTTP(&routingErrorWriter{ResponseWriter: w}, r)
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/health" || r.URL.Path == "/openapi.yaml" || r.URL.Path == "/swagger" || strings.HasPrefix(r.URL.Path, "/swagger/") {
			handler.ServeHTTP(w, r)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/auth/") {
			p, err := s.Authenticate(r)
			if err != nil {
				s.respondError(w, err)
				return
			}
			*r.Context().Value(principalKey{}).(*principal) = p
		}
		if r.Method == http.MethodHead {
			r = r.Clone(r.Context())
			r.Method = http.MethodGet
		}
		validated.ServeHTTP(w, r)
	})
	return s.logRequests(protected), nil
}

func validationDetails(err error) map[string]any {
	violations := []map[string]string{}
	var collect func(error)
	collect = func(err error) {
		switch e := err.(type) {
		case openapi3.MultiError:
			for _, child := range e {
				collect(child)
			}
		case *openapi3filter.RequestError:
			if e.Parameter != nil {
				violations = append(violations, map[string]string{"field": e.Parameter.Name, "message": "Invalid " + e.Parameter.In + " parameter"})
			}
			collect(e.Err)
		case *openapi3.SchemaError:
			field := "body"
			if path := e.JSONPointer(); len(path) > 0 {
				field += "." + strings.Join(path, ".")
			}
			violations = append(violations, map[string]string{"field": field, "message": e.Reason})
		default:
			violations = append(violations, map[string]string{"field": "body", "message": "Invalid or missing request data"})
		}
	}
	collect(err)
	return map[string]any{"violations": violations}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(200)
	}
	return w.ResponseWriter.Write(body)
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Keep monotonic time for durations; convert to UTC only for the timestamp.
		started := time.Now()
		requestID := uuid.NewString()
		w.Header().Set("X-Request-Id", requestID)
		recorder := &statusWriter{ResponseWriter: w}
		p := &principal{}
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		var requestBody any
		mutating := r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE"
		defer func() {
			if recover() != nil {
				writeError(recorder, 500, "INTERNAL_ERROR", "Internal server error", nil)
			}
			status := recorder.status
			if status == 0 {
				status = 200
			}
			var userID any
			if p.ID != 0 {
				userID = p.ID
			}
			fields := []any{"request_id", requestID, "method", r.Method, "endpoint", r.URL.Path, "status_code", status, "duration_ms", float64(time.Since(started).Microseconds()) / 1000, "user_id", userID, "timestamp", started.UTC().Format(time.RFC3339Nano)}
			if mutating {
				fields = append(fields, "request_body", requestBody)
			}
			s.Logger.Info("api_request", fields...)
		}()
		if r.Body != nil {
			body, err := io.ReadAll(http.MaxBytesReader(recorder, r.Body, 1<<20))
			if err != nil {
				writeError(recorder, 400, "VALIDATION_ERROR", "Request body is too large or unreadable", map[string]any{"body": "Maximum 1 MiB"})
				return
			}
			r.Body.Close()
			r.Body = io.NopCloser(bytes.NewReader(body))
			if mutating {
				requestBody = safeBody(body)
			}
		}
		next.ServeHTTP(recorder, r)
	})
}

func safeBody(body []byte) any {
	if len(body) == 0 {
		return nil
	}
	var value any
	if json.Unmarshal(body, &value) != nil {
		return "[invalid JSON]"
	}
	var redact func(any)
	redact = func(value any) {
		switch data := value.(type) {
		case map[string]any:
			for key, child := range data {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "password") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || lower == "authorization" {
					data[key] = "[REDACTED]"
				} else {
					redact(child)
				}
			}
		case []any:
			for _, child := range data {
				redact(child)
			}
		}
	}
	redact(value)
	return value
}
