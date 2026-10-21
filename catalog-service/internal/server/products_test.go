package server

import (
	"encoding/json"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProductOwnership(t *testing.T) {
	own, other := int64(7), int64(8)
	for _, tc := range []struct {
		role      api.Role
		requested *int64
		want      int64
		allowed   bool
	}{
		{api.RoleSELLER, nil, 7, true}, {api.RoleSELLER, &own, 7, true}, {api.RoleSELLER, &other, 8, false}, {api.RoleADMIN, &other, 8, true},
	} {
		got, ok := productOwner(principal{ID: 7, Role: tc.role}, tc.requested)
		if got != tc.want || ok != tc.allowed {
			t.Fatalf("role=%s: got %d,%v", tc.role, got, ok)
		}
	}
}

func TestProductRejectsUnknownSeller(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).productResult(recorder, api.ProductResponse{}, &pgconn.PgError{Code: "23503", ConstraintName: "products_seller_id_fkey"}, 201)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body api.Error
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrorCode != api.VALIDATIONERROR || body.Details == nil || (*body.Details)["seller_id"] == nil {
		t.Fatalf("unexpected error: %+v", body)
	}
}

func TestProductDoesNotMaskUnrelatedForeignKeyError(t *testing.T) {
	recorder := httptest.NewRecorder()
	(&Server{}).productResult(recorder, api.ProductResponse{}, &pgconn.PgError{Code: "23503", ConstraintName: "unrelated_fkey"}, 201)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
