// Colector F1a: ingesta 24/7 de aggTrades de BTCUSDT perp hacia TimescaleDB.
//
// Subcomandos:
//
//	run            migra y arranca el colector (por defecto)
//	migrate        aplica migraciones y sale
//	backfill       puebla histórico desde data.binance.vision: -from/-to (YYYY-MM-DD)
//	refresh-caggs  rematerializa todas las CAggs sobre un rango (-from/-to)
//	coverage       imprime el rango que sirve cada timeframe
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"jputellas.dev/btcdash/collector/internal/api"
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
		minFree := fs.Float64("min-free-gb", 5, "GiB libres mínimos; por debajo, aborta")
		fs.Parse(args)
		from, err := time.Parse("2006-01-02", *fromS)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		to, err := time.Parse("2006-01-02", *toS)
		if err != nil {
			return fmt.Errorf("-to: %w", err)
		}
		backfill.MinFreeBytes = int64(*minFree * (1 << 30))
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return backfill.Run(ctx, pg, cfg.Symbol, from, to, *cache)
	case "backfill-1m":
		fs := flag.NewFlagSet("backfill-1m", flag.ExitOnError)
		fromS := fs.String("from", "2019-09-08", "primer día UTC (YYYY-MM-DD); por defecto el origen del par")
		cache := fs.String("cache", "data/bulk-cache", "directorio de caché de ZIPs")
		fs.Parse(args)
		from, err := time.Parse("2006-01-02", *fromS)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return backfill.Run1m(ctx, pg, binance.NewREST(cfg.FapiBaseURL, cfg.Symbol), cfg.Symbol, from, *cache)
	case "t1":
		fs := flag.NewFlagSet("t1", flag.ExitOnError)
		fromS := fs.String("from", "", "primer día a corregir (YYYY-MM-DD); por defecto, continúa desde el último")
		toS := fs.String("to", "", "último día (YYYY-MM-DD); por defecto, ayer")
		cache := fs.String("cache", "data/bulk-cache", "directorio de caché de ZIPs")
		fs.Parse(args)
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		yesterday := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1)
		to := yesterday
		if *toS != "" {
			if to, err = time.Parse("2006-01-02", *toS); err != nil {
				return fmt.Errorf("-to: %w", err)
			}
		}
		var from time.Time
		if *fromS != "" {
			if from, err = time.Parse("2006-01-02", *fromS); err != nil {
				return fmt.Errorf("-from: %w", err)
			}
		} else {
			if from, err = backfill.LastT1Day(ctx, pg); err != nil {
				return err
			}
			if from.IsZero() {
				from = yesterday // primera ejecución: solo hacia adelante
			}
		}
		return backfill.RunT1(ctx, pg, binance.NewREST(cfg.FapiBaseURL, cfg.Symbol), cfg.Symbol, from, to, *cache)
	case "refresh-caggs":
		fs := flag.NewFlagSet("refresh-caggs", flag.ExitOnError)
		fromS := fs.String("from", "2019-09-08", "primer día UTC (YYYY-MM-DD)")
		toS := fs.String("to", "", "fin exclusivo (YYYY-MM-DD); por defecto, ahora")
		only := fs.String("only", "", "vistas separadas por coma; por defecto, todas")
		fs.Parse(args)
		from, err := time.Parse("2006-01-02", *fromS)
		if err != nil {
			return fmt.Errorf("-from: %w", err)
		}
		to := time.Now().UTC()
		if *toS != "" {
			if to, err = time.Parse("2006-01-02", *toS); err != nil {
				return fmt.Errorf("-to: %w", err)
			}
		}
		var views []string
		if *only != "" {
			views = strings.Split(*only, ",")
		}
		return backfill.RefreshCAggs(ctx, pg, from, to, views)
	case "coverage":
		cov, err := api.Coverage(ctx, pg.Pool, cfg.Symbol)
		if err != nil {
			return err
		}
		for _, c := range cov {
			fmt.Printf("%-4s %-11s %s → %s\n", c.TF, c.Src,
				tstamp(c.First), tstamp(c.Last))
		}
		return nil
	case "resolve-gaps":
		fs := flag.NewFlagSet("resolve-gaps", flag.ExitOnError)
		cache := fs.String("cache", "data/bulk-cache", "directorio de caché de ZIPs")
		fs.Parse(args)
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return backfill.ResolveGaps(ctx, pg, cfg.Symbol, *cache)
	case "run":
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return run(ctx, cfg, pg)
	default:
		return fmt.Errorf("unknown command %q (run|migrate|backfill|backfill-1m|t1|resolve-gaps|refresh-caggs|coverage)", cmd)
	}
}

func tstamp(epoch int64) string {
	if epoch == 0 {
		return "(vacío)"
	}
	return time.Unix(epoch, 0).UTC().Format("2006-01-02 15:04")
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
