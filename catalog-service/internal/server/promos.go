package server

import (
	"database/sql"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"github.com/shopspring/decimal"
	"net/http"
	"time"
)

func promoDiscount(kind string, value, subtotal decimal.Decimal) decimal.Decimal {
	if kind == "PERCENTAGE" {
		if value.GreaterThan(decimal.NewFromInt(70)) {
			return decimal.Zero
		}
		return subtotal.Mul(value).Div(decimal.NewFromInt(100)).Round(2)
	}
	return decimal.Min(value, subtotal).Round(2)
}

func (s *Server) CreatePromoCode(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, api.RoleSELLER, api.RoleADMIN) {
		return
	}
	var in api.PromoCodeCreate
	if !decodeBody(w, r, &in) {
		return
	}
	if !in.ValidUntil.After(in.ValidFrom) {
		writeError(w, 400, "VALIDATION_ERROR", "valid_until must be after valid_from", map[string]any{"valid_until": "must be after valid_from"})
		return
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	var out api.PromoCodeResponse
	err := s.DB.QueryRowContext(r.Context(), `INSERT INTO promo_codes(code,discount_type,discount_value,min_order_amount,max_uses,valid_from,valid_until,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id,code,discount_type,discount_value,min_order_amount,max_uses,current_uses,valid_from,valid_until,active`, in.Code, in.DiscountType, in.DiscountValue, in.MinOrderAmount, in.MaxUses, in.ValidFrom, in.ValidUntil, active).Scan(&out.Id, &out.Code, &out.DiscountType, &out.DiscountValue, &out.MinOrderAmount, &out.MaxUses, &out.CurrentUses, &out.ValidFrom, &out.ValidUntil, &out.Active)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		writeError(w, 400, "VALIDATION_ERROR", "Promo code already exists", map[string]any{"code": "already exists"})
		return
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func applyPromo(r *http.Request, tx *sql.Tx, code *string, id *int64, subtotal decimal.Decimal) (*int64, decimal.Decimal, error) {
	var p api.PromoCodeResponse
	query := `SELECT id,discount_type,discount_value,min_order_amount,max_uses,current_uses,valid_from,valid_until,active FROM promo_codes WHERE code=$1 FOR UPDATE`
	var key any
	if code != nil {
		key = *code
	} else if id != nil {
		key = *id
		query = `SELECT id,discount_type,discount_value,min_order_amount,max_uses,current_uses,valid_from,valid_until,active FROM promo_codes WHERE id=$1 FOR UPDATE`
	} else {
		return nil, decimal.Zero, nil
	}
	err := tx.QueryRowContext(r.Context(), query, key).Scan(&p.Id, &p.DiscountType, &p.DiscountValue, &p.MinOrderAmount, &p.MaxUses, &p.CurrentUses, &p.ValidFrom, &p.ValidUntil, &p.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, decimal.Zero, orderError(422, "PROMO_CODE_INVALID", "Promo code not found")
	}
	if err != nil {
		return nil, decimal.Zero, err
	}
	now := time.Now().UTC()
	if !p.Active || now.Before(p.ValidFrom) || now.After(p.ValidUntil) || (id == nil && p.CurrentUses >= p.MaxUses) {
		return nil, decimal.Zero, orderError(422, "PROMO_CODE_INVALID", "Promo code is inactive, expired or exhausted")
	}
	if subtotal.LessThan(p.MinOrderAmount) {
		if id == nil {
			return nil, decimal.Zero, orderError(422, "PROMO_CODE_MIN_AMOUNT", "Order amount is below promo minimum")
		}
		_, err = tx.ExecContext(r.Context(), `UPDATE promo_codes SET current_uses=current_uses-1 WHERE id=$1`, p.Id)
		return nil, decimal.Zero, err
	}
	if id == nil {
		_, err = tx.ExecContext(r.Context(), `UPDATE promo_codes SET current_uses=current_uses+1 WHERE id=$1`, p.Id)
		if err != nil {
			return nil, decimal.Zero, err
		}
	}
	return &p.Id, promoDiscount(string(p.DiscountType), p.DiscountValue, subtotal), nil
}
