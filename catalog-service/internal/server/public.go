package server

import (
	"bytes"
	"embed"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
)

//go:embed home.html
var homePage string

//go:embed swagger/*
var swaggerFiles embed.FS

func home(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, "index.html", time.Time{}, strings.NewReader(homePage))
}

func specification(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="openapi.yaml"`)
	http.ServeContent(w, r, "openapi.yaml", time.Time{}, bytes.NewReader(api.Specification))
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.DB != nil && s.DB.PingContext(r.Context()) != nil {
		writeError(w, 503, "INTERNAL_ERROR", "Database is unavailable", nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if r.Method != http.MethodHead {
		fmt.Fprintln(w, "ok")
	}
}

type routingErrorWriter struct {
	http.ResponseWriter
	isError bool
}

func (w *routingErrorWriter) WriteHeader(status int) {
	if status == 404 || status == 405 {
		w.isError = true
		if status == 404 {
			writeError(w.ResponseWriter, status, "ROUTE_NOT_FOUND", "Route not found", nil)
		} else {
			writeError(w.ResponseWriter, status, "METHOD_NOT_ALLOWED", "Method not allowed; see the Allow header", nil)
		}
		return
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *routingErrorWriter) Write(body []byte) (int, error) {
	if w.isError {
		return len(body), nil
	}
	return w.ResponseWriter.Write(body)
}

type headWriter struct{ http.ResponseWriter }

func (w headWriter) Write(body []byte) (int, error) { return len(body), nil }
