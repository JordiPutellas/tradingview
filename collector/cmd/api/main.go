// API F1c: velas por timeframe (REST), streaming (WS via LISTEN/NOTIFY),
// dibujos (CRUD) y frontend estático. Solo lecturas sobre TimescaleDB.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"jputellas.dev/btcdash/collector/internal/api"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	srv := api.New(pool, env("SYMBOL", "BTCUSDT"), env("STATIC_DIR", "./static"))
	go srv.Run(ctx) // LISTEN candle_update → broadcast WS

	addr := env("API_ADDR", ":8090")
	hs := &http.Server{Addr: addr, Handler: srv.Handler()}
	go func() {
		<-ctx.Done()
		shCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shCtx)
	}()
	slog.Info("api listening", "addr", addr)
	if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("api", "err", err)
		os.Exit(1)
	}
}
