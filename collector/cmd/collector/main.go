// Colector F1a: ingesta 24/7 de aggTrades de BTCUSDT perp hacia TimescaleDB.
//
// Subcomandos:
//
//	run       migra y arranca el colector (por defecto)
//	migrate   aplica migraciones y sale
//	backfill  puebla histórico desde data.binance.vision: -from/-to (YYYY-MM-DD)
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jputellas.dev/btcdash/collector/internal/backfill"
	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/collect"
	"jputellas.dev/btcdash/collector/internal/config"
	"jputellas.dev/btcdash/collector/internal/health"
	"jputellas.dev/btcdash/collector/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cmd := "run"
	args := os.Args[1:]
	if len(args) > 0 {
		cmd, args = args[0], args[1:]
	}
	if err := dispatch(cmd, args); err != nil {
		slog.Error("fatal", "cmd", cmd, "err", err)
		os.Exit(1)
	}
}

func dispatch(cmd string, args []string) error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	// SIGTERM/SIGINT cancelan el contexto raíz: apagado ordenado (R7).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	pg, err := store.OpenPG(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("db: %w", err)
	}
	defer pg.Pool.Close()

	switch cmd {
	case "migrate":
		return store.Migrate(ctx, pg.Pool)
	case "backfill":
		fs := flag.NewFlagSet("backfill", flag.ExitOnError)
		fromS := fs.String("from", "", "primer día UTC (YYYY-MM-DD)")
		toS := fs.String("to", "", "último día UTC inclusive (YYYY-MM-DD)")
		cache := fs.String("cache", "data/bulk-cache", "directorio de caché de ZIPs")
		fs.Parse(args)
		from, err := time.Parse("2006-01-02", *fromS)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		to, err := time.Parse("2006-01-02", *toS)
		if err != nil {
			return fmt.Errorf("-to: %w", err)
		}
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return backfill.Run(ctx, pg, cfg.Symbol, from, to, *cache)
	case "run":
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return run(ctx, cfg, pg)
	default:
		return fmt.Errorf("unknown command %q (run|migrate|backfill)", cmd)
	}
}

func run(ctx context.Context, cfg config.Config, pg *store.PG) error {
	h := health.New(cfg.FreshnessMax)
	h.OpenGaps = func(ctx context.Context) (int, error) { return pg.OpenGapCount(ctx, cfg.Symbol) }

	ws := &binance.WSClient{
		BaseURL:      cfg.WSBaseURL,
		Symbol:       cfg.Symbol,
		OnConnect:    func() { h.SetWSConnected(true) },
		OnDisconnect: func(error) { h.SetWSConnected(false) },
	}
	col := &collect.Collector{
		Symbol:          cfg.Symbol,
		Store:           pg,
		Rest:            binance.NewREST(cfg.FapiBaseURL, cfg.Symbol),
		WS:              ws,
		Health:          h,
		ReconcileWindow: cfg.ReconcileWindow,
		BufferSize:      cfg.BufferSize,
	}
	h.BufferLen = col.BufferLen

	go h.Serve(ctx, cfg.HealthAddr)
	go h.RunPinger(ctx, cfg.HealthcheckURL, cfg.HealthcheckInterval)

	slog.Info("collector starting", "symbol", cfg.Symbol,
		"reconcile_window", cfg.ReconcileWindow, "health_addr", cfg.HealthAddr)
	return col.Run(ctx)
}
