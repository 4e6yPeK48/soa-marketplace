package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/mchupryna/soa-marketplace/catalog-service/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("startup failed", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	minutes, err := strconv.Atoi(env("ORDER_COOLDOWN_MINUTES", "1"))
	if err != nil || minutes < 1 || minutes > 1440 {
		return errors.New("ORDER_COOLDOWN_MINUTES must be between 1 and 1440")
	}
	db, err := sql.Open("pgx", os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = db.PingContext(startup); err != nil {
		return err
	}
	s := &server.Server{DB: db, JWTSecret: []byte(secret), OrderCooldown: time.Duration(minutes) * time.Minute, Logger: logger}
	if email := os.Getenv("ADMIN_EMAIL"); email != "" {
		if len(os.Getenv("ADMIN_PASSWORD")) < 8 {
			return errors.New("ADMIN_PASSWORD must contain at least 8 bytes")
		}
		if err = s.BootstrapAdmin(startup, email, os.Getenv("ADMIN_PASSWORD")); err != nil {
			return err
		}
	}
	handler, err := server.New(s)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Addr: ":8080", Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	logger.Info("marketplace listening", "address", httpServer.Addr)
	if err = httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
