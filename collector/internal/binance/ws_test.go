package binance

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"jputellas.dev/btcdash/collector/internal/candle"
)

// Simula la caída del WS: el servidor entrega 3 trades y corta en seco. El
// cliente debe reconectar solo y seguir recibiendo (R2, test de reconexión).
func TestWSClientReconnects(t *testing.T) {
	var conns atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		n := conns.Add(1)
		ctx := r.Context()
		for i := int64(0); i < 3; i++ {
			id := (n-1)*3 + i + 1
			msg := fmt.Sprintf(`{"e":"aggTrade","E":123,"s":"BTCUSDT","a":%d,"p":"64000.1","q":"0.5","f":%d,"l":%d,"T":%d,"m":false}`,
				id, id*10, id*10, 1_700_000_000_000+id*100)
			if err := c.Write(ctx, websocket.MessageText, []byte(msg)); err != nil {
				return
			}
		}
		// Corte abrupto sin close frame: el cliente debe tratarlo como caída.
		c.CloseNow()
	}))
	defer srv.Close()

	client := &WSClient{
		BaseURL:         "ws" + strings.TrimPrefix(srv.URL, "http"),
		Symbol:          "BTCUSDT",
		ReadIdleTimeout: 5 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	got := make(chan candle.AggTrade, 64)
	go client.Run(ctx, func(tr candle.AggTrade) { got <- tr })

	var ids []int64
	for len(ids) < 6 {
		select {
		case tr := <-got:
			ids = append(ids, tr.ID)
			if tr.Price != 64000_10000000 || tr.Qty != 50000000 {
				t.Fatalf("bad fixed-point parse: %+v", tr)
			}
		case <-ctx.Done():
			t.Fatalf("timeout: got %d trades over %d connections", len(ids), conns.Load())
		}
	}
	cancel()
	if conns.Load() < 2 {
		t.Fatalf("expected at least 2 connections (reconnect), got %d", conns.Load())
	}
	for i, id := range ids {
		if id != int64(i+1) {
			t.Fatalf("ids out of order: %v", ids)
		}
	}
}
