// Package config lee la configuración del colector desde variables de entorno.
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	DatabaseURL string
	Symbol      string // "BTCUSDT"
	WSBaseURL   string // wss://fstream.binance.com/ws
	FapiBaseURL string // https://fapi.binance.com

	// ReconcileWindow: edad máxima de un hueco para intentar REST. La API
	// real corta a ~48 h (trampa 10); 40 h deja margen de seguridad.
	ReconcileWindow time.Duration

	HealthAddr          string
	HealthcheckURL      string // ping saliente (healthchecks.io); vacío = off
	HealthcheckInterval time.Duration
	FreshnessMax        time.Duration // frescura mínima para considerarse sano
	BufferSize          int           // velas en cola hacia la BD
}

func FromEnv() (Config, error) {
	c := Config{
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		Symbol:              getdef("SYMBOL", "BTCUSDT"),
		// OJO: ruta /market/ws, no /ws. Desde el 2026-03-06 las conexiones
		// sin ruta solo reciben streams "public" y @aggTrade calla EN
		// SILENCIO (trampa 12 del README).
		WSBaseURL:           getdef("WS_BASE_URL", "wss://fstream.binance.com/market/ws"),
		FapiBaseURL:         getdef("FAPI_BASE_URL", "https://fapi.binance.com"),
		HealthAddr:          getdef("HEALTH_ADDR", ":8080"),
		HealthcheckURL:      os.Getenv("HEALTHCHECK_URL"),
		ReconcileWindow:     durdef("RECONCILE_WINDOW", 40*time.Hour),
		HealthcheckInterval: durdef("HEALTHCHECK_INTERVAL", time.Minute),
		FreshnessMax:        durdef("FRESHNESS_MAX", 30*time.Second),
		BufferSize:          intdef("BUFFER_SIZE", 262144),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

func getdef(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func durdef(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func intdef(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
