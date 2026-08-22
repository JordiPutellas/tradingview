package api

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Cobertura mínima exigible a cada timeframe. Absoluta a propósito: la
// retención es infinita (README, sec. 6), así que estos umbrales no caducan.
//   - candles_1s: el backfill de F1b cubrió los 2 años previos, desde el
//     2024-08-21.
//   - candles_1m y todo lo derivado: desde el origen del par, 2019-09-08.
var (
	first1s = time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
	first1m = time.Date(2019, 10, 1, 0, 0, 0, 0, time.UTC)
)

// TestTimeframeCoverage comprueba que los 24 timeframes sirven el histórico
// completo, no solo la ventana de refresco de su CAgg (bug de F2a).
//
//	DATABASE_URL=postgres://btc:...@127.0.0.1:5433/btc go test ./internal/api -run Coverage -v
func TestTimeframeCoverage(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("sin DATABASE_URL: test de cobertura contra la BD real")
	}
	symbol := os.Getenv("SYMBOL")
	if symbol == "" {
		symbol = "BTCUSDT"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("conexión: %v", err)
	}
	defer pool.Close()

	cov, err := Coverage(ctx, pool, symbol)
	if err != nil {
		t.Fatalf("coverage: %v", err)
	}
	now := time.Now().UTC()
	for i, c := range cov {
		tf := Timeframes[i]
		if c.First == 0 || c.Last == 0 {
			t.Errorf("%s (%s): sin velas", c.TF, c.Src)
			continue
		}
		first, last := time.Unix(c.First, 0).UTC(), time.Unix(c.Last, 0).UTC()
		want := first1m
		if tf.Src == "candles_1s" {
			want = first1s
		}
		t.Logf("%-4s %-11s %s → %s", c.TF, c.Src,
			first.Format("2006-01-02 15:04"), last.Format("2006-01-02 15:04"))
		if first.After(want) {
			t.Errorf("%s (%s): histórico empieza en %s, se esperaba <= %s",
				c.TF, c.Src, first.Format(time.RFC3339), want.Format("2006-01-02"))
		}
		// Frescura: el último bucket servible nunca puede quedar más de tres
		// buckets atrás (margen para el bucket en curso y el end_offset).
		stale := 3 * time.Duration(tf.Seconds) * time.Second
		if stale < 5*time.Minute {
			stale = 5 * time.Minute
		}
		if last.Before(now.Add(-stale)) {
			t.Errorf("%s: última vela %s, más de 3 buckets atrás", c.TF, last.Format(time.RFC3339))
		}
	}
}

// TestDeepHistoryRequest cubre el 500 que aparecía al pedir más pasado en los
// timeframes grandes: sin `from`, el handler calculaba `to - limit*bucket*3`,
// que para 12M x 5000 velas cae en el año -47.000 y PostgreSQL rechaza el
// to_timestamp(). Lo veía el usuario al desplazarse al inicio del histórico.
func TestDeepHistoryRequest(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("sin DATABASE_URL: test contra la BD real")
	}
	symbol := os.Getenv("SYMBOL")
	if symbol == "" {
		symbol = "BTCUSDT"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("conexión: %v", err)
	}
	defer pool.Close()
	srv := New(pool, symbol, ".")
	h := srv.Handler()
	// El principio del histórico: 2019-01-01, anterior a cualquier vela.
	for _, tf := range []string{"12M", "6M", "3M", "1M", "1W", "1D", "1h", "1m", "1s"} {
		req := httptest.NewRequest("GET", "/api/candles?tf="+tf+"&to=1546300800&limit=5000", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		if rec.Code != 200 {
			t.Errorf("%s: HTTP %d — %s", tf, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}
}
