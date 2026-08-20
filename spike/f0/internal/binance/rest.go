package binance

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

const fapiBase = "https://fapi.binance.com"

// restAggTrade mirrors the /fapi/v1/aggTrades JSON payload. Price/qty are
// kept as raw strings to preserve exactly what the API returned.
type restAggTrade struct {
	A int64  `json:"a"`
	P string `json:"p"`
	Q string `json:"q"`
	F int64  `json:"f"`
	L int64  `json:"l"`
	T int64  `json:"T"`
	M bool   `json:"m"`
}

// Client is a minimal throttled fapi client. /fapi/v1/aggTrades weighs 20 and
// the IP limit is 2400 weight/min, so we pace requests ~550ms apart
// (~109 req/min ≈ 2180 weight/min) and back off on 429/418.
type Client struct {
	HTTP     *http.Client
	Requests int
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) fetch(params string) ([]restAggTrade, error) {
	url := fapiBase + "/fapi/v1/aggTrades?symbol=BTCUSDT&limit=1000&" + params
	for attempt := 1; ; attempt++ {
		time.Sleep(550 * time.Millisecond)
		resp, err := c.HTTP.Get(url)
		if err != nil {
			if attempt >= 5 {
				return nil, err
			}
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
			continue
		}
		c.Requests++
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch {
		case resp.StatusCode == 200:
			var out []restAggTrade
			if err := json.Unmarshal(body, &out); err != nil {
				return nil, fmt.Errorf("bad JSON from %s: %w", url, err)
			}
			return out, nil
		case resp.StatusCode == 429 || resp.StatusCode == 418:
			wait := 60 * time.Second
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if s, err := strconv.Atoi(ra); err == nil {
					wait = time.Duration(s+1) * time.Second
				}
			}
			fmt.Fprintf(os.Stderr, "rate limited (%d), sleeping %s\n", resp.StatusCode, wait)
			time.Sleep(wait)
		case resp.StatusCode >= 500 && attempt < 5:
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		default:
			return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, url, body)
		}
	}
}

// DownloadDayFromID downloads one UTC day of aggTrades paginating by fromId
// (the CORRECT strategy): seed with startTime, then fromId = lastId+1.
func (c *Client) DownloadDayFromID(dayStartMs, dayEndMs int64, outPath string) (int, error) {
	w, err := newAggCSVWriter(outPath)
	if err != nil {
		return 0, err
	}
	defer w.close()
	batch, err := c.fetch(fmt.Sprintf("startTime=%d", dayStartMs))
	if err != nil {
		return 0, err
	}
	total := 0
	for {
		if len(batch) == 0 {
			break
		}
		done := false
		for _, t := range batch {
			if t.T > dayEndMs {
				done = true
				break
			}
			if err := w.write(t); err != nil {
				return total, err
			}
			total++
		}
		if done || len(batch) < 1000 {
			break
		}
		lastID := batch[len(batch)-1].A
		if batch, err = c.fetch(fmt.Sprintf("fromId=%d", lastID+1)); err != nil {
			return total, err
		}
	}
	return total, w.closeErr()
}

// DownloadDayStartTime downloads the same day paginating by
// startTime = lastT + 1 (the DELIBERATELY WRONG strategy): any trades sharing
// the last returned millisecond beyond the page limit are silently skipped.
func (c *Client) DownloadDayStartTime(dayStartMs, dayEndMs int64, outPath string) (int, error) {
	w, err := newAggCSVWriter(outPath)
	if err != nil {
		return 0, err
	}
	defer w.close()
	cursor := dayStartMs
	total := 0
	for {
		batch, err := c.fetch(fmt.Sprintf("startTime=%d", cursor))
		if err != nil {
			return total, err
		}
		if len(batch) == 0 {
			break
		}
		done := false
		for _, t := range batch {
			if t.T > dayEndMs {
				done = true
				break
			}
			if err := w.write(t); err != nil {
				return total, err
			}
			total++
		}
		if done {
			break
		}
		cursor = batch[len(batch)-1].T + 1
	}
	return total, w.closeErr()
}

type aggCSVWriter struct {
	f   *os.File
	cw  *csv.Writer
	err error
}

func newAggCSVWriter(path string) (*aggCSVWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	cw := csv.NewWriter(f)
	cw.Write([]string{"agg_trade_id", "price", "quantity", "first_trade_id", "last_trade_id", "transact_time", "is_buyer_maker"})
	return &aggCSVWriter{f: f, cw: cw}, nil
}

func (w *aggCSVWriter) write(t restAggTrade) error {
	return w.cw.Write([]string{
		strconv.FormatInt(t.A, 10), t.P, t.Q,
		strconv.FormatInt(t.F, 10), strconv.FormatInt(t.L, 10),
		strconv.FormatInt(t.T, 10), strconv.FormatBool(t.M),
	})
}

func (w *aggCSVWriter) close() { w.cw.Flush(); w.f.Close() }
func (w *aggCSVWriter) closeErr() error {
	w.cw.Flush()
	if err := w.cw.Error(); err != nil {
		return err
	}
	return w.f.Close()
}
