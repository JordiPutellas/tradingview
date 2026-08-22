// Motor de alertas de precio (F5, bloque 3).
//
// Proceso APARTE del colector a propósito: el colector es la ingesta 24/7 y
// nada que hable con internet —un POST a Telegram que se cuelgue, por
// ejemplo— puede compartir su bucle. Este proceso solo lee candles_1s y
// escribe en alerts / alert_events / alert_engine; si se cae, la ingesta ni se
// entera y al volver reanuda por la marca de agua.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"jputellas.dev/btcdash/collector/internal/alerts"
	"jputellas.dev/btcdash/collector/internal/store"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func dur(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		slog.Error("DATABASE_URL is required")
		os.Exit(1)
	}
	pg, err := store.OpenPG(ctx, url)
	if err != nil {
		slog.Error("db", "err", err)
		os.Exit(1)
	}
	defer pg.Pool.Close()

	tg := alerts.TelegramDesdeEnv()
	if !tg.Configurado() {
		// Igual que con HEALTHCHECK_URL en F1b: lo que no avisa tiene que
		// avisar de que no avisa.
		slog.Warn("TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID vacíos: las alertas se " +
			"registran y se ven en el panel, pero NO se notifican (ver RUNBOOK)")
	}
	m := &alerts.Motor{
		PG:         pg,
		Symbol:     env("SYMBOL", "BTCUSDT"),
		TG:         tg,
		ReplayMax:  dur("ALERT_REPLAY_MAX", 2*time.Hour),
		Vigilancia: dur("ALERT_WATCHDOG", 15*time.Second),
	}
	if err := m.Run(ctx); err != nil {
		slog.Error("motor de alertas", "err", err)
		os.Exit(1)
	}
}
