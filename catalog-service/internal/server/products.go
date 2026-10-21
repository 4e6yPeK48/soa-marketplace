package server

import (
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"net/http"

	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
)

const productColumns = `id,seller_id,name,description,price,stock,category,status,created_at,updated_at`

type productScanner interface{ Scan(...any) error }

func scanProduct(row productScanner) (api.ProductResponse, error) {
	var p api.ProductResponse
	err := row.Scan(&p.Id, &p.SellerId, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Category, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}
func (s *Server) productResult(w http.ResponseWriter, p api.ProductResponse, err error, status int) {
	var constraint *pgconn.PgError
	if errors.As(err, &constraint) && constraint.Code == "23503" && constraint.ConstraintName == "products_seller_id_fkey" {
		writeError(w, 400, "VALIDATION_ERROR", "Seller does not exist", map[string]any{"seller_id": "must reference an existing user"})
		return
	}

	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, 404, "PRODUCT_NOT_FOUND", "Product not found", nil)
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, status, p)
}
func productOwner(p principal, requested *int64) (int64, bool) {
	if requested == nil {
		return p.ID, true
	}
	return *requested, p.Role == api.RoleADMIN || *requested == p.ID
}
func (s *Server) CreateProduct(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, api.RoleSELLER, api.RoleADMIN) {
		return
	}
	var input api.ProductCreate
	if !decodeBody(w, r, &input) {
		return
	}
	owner, ok := productOwner(currentUser(r), input.SellerId)
	if !ok {
		writeError(w, 403, "ACCESS_DENIED", "Cannot create another seller's product", nil)
		return
	}
	p, err := scanProduct(s.DB.QueryRowContext(r.Context(), `INSERT INTO products(seller_id,name,description,price,stock,category,status) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+productColumns, owner, input.Name, input.Description, input.Price, input.Stock, input.Category, input.Status))
	s.productResult(w, p, err, 201)
}
func (s *Server) GetProduct(w http.ResponseWriter, r *http.Request, id int64) {
	p, err := scanProduct(s.DB.QueryRowContext(r.Context(), `SELECT `+productColumns+` FROM products WHERE id=$1`, id))
	s.productResult(w, p, err, 200)
}
func (s *Server) ListProducts(w http.ResponseWriter, r *http.Request, params api.ListProductsParams) {
	page, size := int32(0), int32(20)
	if params.Page != nil {
		page = *params.Page
	}
	if params.Size != nil {
		size = *params.Size
	}
	if page < 0 || size < 1 || size > 100 {
		writeError(w, 400, "VALIDATION_ERROR", "Invalid pagination", map[string]any{"page": "must be >= 0", "size": "must be 1 to 100"})
		return
	}
	const filter = ` WHERE ($1::text IS NULL OR status=$1) AND ($2::text IS NULL OR category=$2)`
	rows, err := s.DB.QueryContext(r.Context(), `SELECT `+productColumns+` FROM products`+filter+` ORDER BY id LIMIT $3 OFFSET $4`, params.Status, params.Category, size, int64(page)*int64(size))
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer rows.Close()
	result := api.ProductList{Items: []api.ProductResponse{}, Page: page, Size: size}
	for rows.Next() {
		var p api.ProductResponse
		err = rows.Scan(&p.Id, &p.SellerId, &p.Name, &p.Description, &p.Price, &p.Stock, &p.Category, &p.Status, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			s.respondError(w, err)
			return
		}
		result.Items = append(result.Items, p)
	}
	if err = rows.Err(); err != nil {
		s.respondError(w, err)
		return
	}
	err = s.DB.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM products`+filter, params.Status, params.Category).Scan(&result.TotalElements)
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 200, result)
}
func (s *Server) UpdateProduct(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleSELLER, api.RoleADMIN) {
		return
	}
	var input api.ProductUpdate
	if !decodeBody(w, r, &input) {
		return
	}
	user := currentUser(r)
	owner, ok := productOwner(user, input.SellerId)
	if !ok {
		writeError(w, 403, "ACCESS_DENIED", "Cannot transfer product ownership", nil)
		return
	}
	// Ownership is part of the write predicate, preventing a check/write race.
	p, err := scanProduct(s.DB.QueryRowContext(r.Context(), `UPDATE products SET name=$2,description=$3,price=$4,stock=$5,category=$6,status=$7,seller_id=CASE WHEN $8::bigint IS NULL THEN seller_id ELSE $8 END WHERE id=$1 AND (seller_id=$9 OR $10) RETURNING `+productColumns, id, input.Name, input.Description, input.Price, input.Stock, input.Category, input.Status, nullableOwner(input.SellerId, owner), user.ID, user.Role == api.RoleADMIN))
	if errors.Is(err, sql.ErrNoRows) {
		s.productWriteMissing(w, r, id)
		return
	}
	s.productResult(w, p, err, 200)
}
func nullableOwner(requested *int64, owner int64) any {
	if requested == nil {
		return nil
	}
	return owner
}
func (s *Server) DeleteProduct(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleSELLER, api.RoleADMIN) {
		return
	}
	user := currentUser(r)
	result, err := s.DB.ExecContext(r.Context(), `UPDATE products SET status='ARCHIVED' WHERE id=$1 AND (seller_id=$2 OR $3)`, id, user.ID, user.Role == api.RoleADMIN)
	if err != nil {
		s.respondError(w, err)
		return
	}
	count, err := result.RowsAffected()
	if err != nil {
		s.respondError(w, err)
		return
	}
	if count == 0 {
		s.productWriteMissing(w, r, id)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) productWriteMissing(w http.ResponseWriter, r *http.Request, id int64) {
	var exists bool
	err := s.DB.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM products WHERE id=$1)`, id).Scan(&exists)
	if err != nil {
		s.respondError(w, err)
		return
	}
	if exists {
		writeError(w, 403, "ACCESS_DENIED", "Product belongs to another seller", nil)
		return
	}
	writeError(w, 404, "PRODUCT_NOT_FOUND", "Product not found", nil)
}
