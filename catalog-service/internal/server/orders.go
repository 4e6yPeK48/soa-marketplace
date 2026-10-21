package server

import (
	"database/sql"
	"errors"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/api"
	"github.com/shopspring/decimal"
	"math"
	"net/http"
	"sort"
	"time"
)

func orderError(status int, code, message string) error {
	return &apiError{Status: status, Code: code, Message: message}
}
func validOrderTransition(from, to api.OrderStatus) bool {
	return (from == "CREATED" && to == "PAYMENT_PENDING") || (from == "PAYMENT_PENDING" && to == "PAID") || (from == "PAID" && to == "SHIPPED") || (from == "SHIPPED" && to == "COMPLETED")
}

func validateItems(w http.ResponseWriter, items []api.OrderItemInput) bool {
	seen := map[int64]bool{}
	for _, item := range items {
		if seen[item.ProductId] {
			writeError(w, 400, "VALIDATION_ERROR", "Duplicate product_id", map[string]any{"items": "product_id must be unique"})
			return false
		}
		seen[item.ProductId] = true
	}
	return true
}

func validateOrderAmounts(total, discount decimal.Decimal) error {
	maximum := decimal.RequireFromString("9999999999.99")
	if total.GreaterThan(maximum) || discount.GreaterThan(maximum) {
		return &apiError{Status: 400, Code: "VALIDATION_ERROR", Message: "Order amounts exceed maximum amount", Details: map[string]any{"items": "total_amount and discount_amount must not exceed 9999999999.99"}}
	}
	return nil
}

func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	if !s.allowed(w, r, api.RoleUSER, api.RoleADMIN) {
		return
	}
	var in api.OrderCreate
	if !decodeBody(w, r, &in) || !validateItems(w, in.Items) {
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer tx.Rollback()
	user := currentUser(r)
	if err = lockOrderOwner(r, tx, user.ID); err == nil {
		err = s.checkOrderCooldown(r, tx, user.ID, "CREATE_ORDER")
	}
	var active bool
	if err == nil {
		err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status IN ('CREATED','PAYMENT_PENDING'))`, user.ID).Scan(&active)
		if err == nil && active {
			err = orderError(409, "ORDER_HAS_ACTIVE", "User already has an active order")
		}
	}
	var items []api.OrderItemResponse
	var subtotal decimal.Decimal
	if err == nil {
		items, subtotal, err = reserveItems(r, tx, nil, in.Items)
	}
	var promoID *int64
	discount := decimal.Zero
	if err == nil {
		promoID, discount, err = applyPromo(r, tx, in.PromoCode, nil, subtotal)
	}
	if err == nil {
		err = validateOrderAmounts(subtotal.Sub(discount), discount)
	}
	var out api.OrderResponse
	if err == nil {
		out.Items = items
		err = tx.QueryRowContext(r.Context(), `INSERT INTO orders(user_id,status,promo_code_id,total_amount,discount_amount) VALUES($1,'CREATED',$2,$3,$4) RETURNING id,user_id,status,promo_code_id,total_amount,discount_amount,created_at,updated_at`, user.ID, promoID, subtotal.Sub(discount), discount).Scan(&out.Id, &out.UserId, &out.Status, &out.PromoCodeId, &out.TotalAmount, &out.DiscountAmount, &out.CreatedAt, &out.UpdatedAt)
	}
	if err == nil {
		err = saveOrderItems(r, tx, out.Id, items)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO user_operations(user_id,operation_type,created_at) VALUES($1,'CREATE_ORDER',clock_timestamp())`, user.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 201, out)
}

func lockOrderOwner(r *http.Request, tx *sql.Tx, id int64) error {
	var ignored int64
	return tx.QueryRowContext(r.Context(), `SELECT id FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&ignored)
}
func (s *Server) checkOrderCooldown(r *http.Request, tx *sql.Tx, id int64, operation string) error {
	var last sql.NullTime
	if err := tx.QueryRowContext(r.Context(), `SELECT max(created_at) FROM user_operations WHERE user_id=$1 AND operation_type=$2`, id, operation).Scan(&last); err != nil {
		return err
	}
	if last.Valid {
		return cooldownError(last.Time, time.Now(), s.OrderCooldown)
	}
	return nil
}

func cooldownError(last, now time.Time, interval time.Duration) error {
	remaining := last.Add(interval).Sub(now)
	if remaining <= 0 {
		return nil
	}
	seconds := max(1, int(math.Ceil(remaining.Seconds())))
	return &apiError{Status: 429, Code: "ORDER_LIMIT_EXCEEDED", Message: "Please wait before repeating this order operation", Details: map[string]any{"retry_after_seconds": seconds}}
}

// Lock the owner before the order so create/update/cancel use the same lock order.
func loadOrder(r *http.Request, tx *sql.Tx, id int64, lock bool) (api.OrderResponse, error) {
	var out api.OrderResponse
	if lock {
		var owner int64
		err := tx.QueryRowContext(r.Context(), `SELECT user_id FROM orders WHERE id=$1`, id).Scan(&owner)
		if errors.Is(err, sql.ErrNoRows) {
			return out, orderError(404, "ORDER_NOT_FOUND", "Order not found")
		}
		if err != nil {
			return out, err
		}
		if err = lockOrderOwner(r, tx, owner); err != nil {
			return out, err
		}
	}
	query := `SELECT id,user_id,status,promo_code_id,total_amount,discount_amount,created_at,updated_at FROM orders WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := tx.QueryRowContext(r.Context(), query, id).Scan(&out.Id, &out.UserId, &out.Status, &out.PromoCodeId, &out.TotalAmount, &out.DiscountAmount, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, orderError(404, "ORDER_NOT_FOUND", "Order not found")
	}
	if err != nil {
		return out, err
	}
	user := currentUser(r)
	if user.Role != api.RoleADMIN && out.UserId != user.ID {
		return out, orderError(403, "ORDER_OWNERSHIP_VIOLATION", "Order belongs to another user")
	}
	rows, err := tx.QueryContext(r.Context(), `SELECT product_id,quantity,price_at_order FROM order_items WHERE order_id=$1 ORDER BY product_id`, id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	out.Items = []api.OrderItemResponse{}
	for rows.Next() {
		var item api.OrderItemResponse
		if err = rows.Scan(&item.ProductId, &item.Quantity, &item.PriceAtOrder); err != nil {
			return out, err
		}
		out.Items = append(out.Items, item)
	}
	return out, rows.Err()
}
func (s *Server) GetOrder(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleUSER, api.RoleADMIN) {
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer tx.Rollback()
	out, err := loadOrder(r, tx, id, false)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

type orderProduct struct {
	price  decimal.Decimal
	stock  int64
	status string
}

func reserveItems(r *http.Request, tx *sql.Tx, old []api.OrderItemResponse, input []api.OrderItemInput) ([]api.OrderItemResponse, decimal.Decimal, error) {
	ids := map[int64]bool{}
	returned := map[int64]int64{}
	for _, item := range old {
		ids[item.ProductId] = true
		returned[item.ProductId] += int64(item.Quantity)
	}
	for _, item := range input {
		ids[item.ProductId] = true
	}
	sorted := make([]int64, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	products := map[int64]orderProduct{}
	for _, id := range sorted {
		var p orderProduct
		err := tx.QueryRowContext(r.Context(), `SELECT price,stock,status FROM products WHERE id=$1 FOR UPDATE`, id).Scan(&p.price, &p.stock, &p.status)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, decimal.Zero, err
		}
		p.stock += returned[id]
		products[id] = p
	}
	for _, item := range input {
		p, ok := products[item.ProductId]
		if !ok {
			return nil, decimal.Zero, orderError(404, "PRODUCT_NOT_FOUND", "Product not found")
		}
		if p.status != "ACTIVE" {
			return nil, decimal.Zero, orderError(409, "PRODUCT_INACTIVE", "Product is not active")
		}
	}
	deficits := []map[string]any{}
	for _, item := range input {
		p := products[item.ProductId]
		if p.stock < int64(item.Quantity) {
			deficits = append(deficits, map[string]any{"product_id": item.ProductId, "requested": item.Quantity, "available": p.stock})
		}
	}
	if len(deficits) > 0 {
		return nil, decimal.Zero, &apiError{Status: 409, Code: "INSUFFICIENT_STOCK", Message: "Insufficient product stock", Details: map[string]any{"products": deficits}}
	}
	subtotal := decimal.Zero
	items := make([]api.OrderItemResponse, 0, len(input))
	for _, item := range input {
		p := products[item.ProductId]
		p.stock -= int64(item.Quantity)
		products[item.ProductId] = p
		items = append(items, api.OrderItemResponse{ProductId: item.ProductId, Quantity: item.Quantity, PriceAtOrder: p.price})
		subtotal = subtotal.Add(p.price.Mul(decimal.NewFromInt(int64(item.Quantity))))
	}
	for _, id := range sorted {
		p, ok := products[id]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(r.Context(), `UPDATE products SET stock=$2 WHERE id=$1`, id, p.stock); err != nil {
			return nil, decimal.Zero, err
		}
	}
	return items, subtotal, nil
}
func saveOrderItems(r *http.Request, tx *sql.Tx, id int64, items []api.OrderItemResponse) error {
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM order_items WHERE order_id=$1`, id); err != nil {
		return err
	}
	for _, item := range items {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO order_items(order_id,product_id,quantity,price_at_order) VALUES($1,$2,$3,$4)`, id, item.ProductId, item.Quantity, item.PriceAtOrder); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) UpdateOrder(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleUSER, api.RoleADMIN) {
		return
	}
	var in api.OrderUpdate
	if !decodeBody(w, r, &in) || !validateItems(w, in.Items) {
		return
	}
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer tx.Rollback()
	out, err := loadOrder(r, tx, id, true)
	if err == nil && out.Status != "CREATED" {
		err = &apiError{Status: 409, Code: "INVALID_STATE_TRANSITION", Message: "Only a newly created order can be updated", Details: map[string]any{"current_status": out.Status, "requested_status": "UPDATE", "allowed_statuses": []api.OrderStatus{"CREATED"}}}
	}
	if err == nil {
		err = s.checkOrderCooldown(r, tx, out.UserId, "UPDATE_ORDER")
	}
	var items []api.OrderItemResponse
	var subtotal, discount decimal.Decimal
	var promoID *int64
	if err == nil {
		items, subtotal, err = reserveItems(r, tx, out.Items, in.Items)
	}
	if err == nil {
		promoID, discount, err = applyPromo(r, tx, nil, out.PromoCodeId, subtotal)
	}
	if err == nil {
		err = validateOrderAmounts(subtotal.Sub(discount), discount)
	}
	if err == nil {
		err = saveOrderItems(r, tx, id, items)
	}
	if err == nil {
		out.Items = items
		out.PromoCodeId = promoID
		out.DiscountAmount = discount
		out.TotalAmount = subtotal.Sub(discount)
		err = tx.QueryRowContext(r.Context(), `UPDATE orders SET promo_code_id=$2,total_amount=$3,discount_amount=$4 WHERE id=$1 RETURNING updated_at`, id, promoID, out.TotalAmount, discount).Scan(&out.UpdatedAt)
	}
	if err == nil {
		_, err = tx.ExecContext(r.Context(), `INSERT INTO user_operations(user_id,operation_type,created_at) VALUES($1,'UPDATE_ORDER',clock_timestamp())`, out.UserId)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleUSER, api.RoleADMIN) {
		return
	}
	s.mutateOrderStatus(w, r, id, "CANCELED")
}
func (s *Server) ChangeOrderStatus(w http.ResponseWriter, r *http.Request, id int64) {
	if !s.allowed(w, r, api.RoleADMIN) {
		return
	}
	var in api.OrderStatusUpdate
	if !decodeBody(w, r, &in) {
		return
	}
	s.mutateOrderStatus(w, r, id, in.Status)
}
func (s *Server) mutateOrderStatus(w http.ResponseWriter, r *http.Request, id int64, status api.OrderStatus) {
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		s.respondError(w, err)
		return
	}
	defer tx.Rollback()
	out, err := loadOrder(r, tx, id, true)
	if err == nil {
		if status == "CANCELED" {
			if out.Status != "CREATED" && out.Status != "PAYMENT_PENDING" {
				err = &apiError{Status: 409, Code: "INVALID_STATE_TRANSITION", Message: "Only a new order or an order awaiting payment can be canceled", Details: map[string]any{"current_status": out.Status, "requested_status": status, "allowed_statuses": []api.OrderStatus{"CREATED", "PAYMENT_PENDING"}}}
			}
		} else if !validOrderTransition(out.Status, status) {
			allowed := []api.OrderStatus{}
			switch out.Status {
			case "CREATED":
				allowed = []api.OrderStatus{"PAYMENT_PENDING", "CANCELED"}
			case "PAYMENT_PENDING":
				allowed = []api.OrderStatus{"PAID", "CANCELED"}
			case "PAID":
				allowed = []api.OrderStatus{"SHIPPED"}
			case "SHIPPED":
				allowed = []api.OrderStatus{"COMPLETED"}
			}
			err = &apiError{Status: 409, Code: "INVALID_STATE_TRANSITION", Message: "Order cannot move to the requested status", Details: map[string]any{"current_status": out.Status, "requested_status": status, "allowed_statuses": allowed}}
		}
	}
	if err == nil && status == "CANCELED" {
		// Stored items are sorted by product ID, matching reservation lock order.
		for _, item := range out.Items {
			_, err = tx.ExecContext(r.Context(), `UPDATE products SET stock=stock+$2 WHERE id=$1`, item.ProductId, item.Quantity)
			if err != nil {
				break
			}
		}
		if err == nil && out.PromoCodeId != nil {
			_, err = tx.ExecContext(r.Context(), `UPDATE promo_codes SET current_uses=current_uses-1 WHERE id=$1`, *out.PromoCodeId)
		}
	}
	if err == nil {
		out.Status = status
		err = tx.QueryRowContext(r.Context(), `UPDATE orders SET status=$2 WHERE id=$1 RETURNING updated_at`, id, status).Scan(&out.UpdatedAt)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.respondError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
