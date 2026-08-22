package alerts

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"jputellas.dev/btcdash/collector/internal/store"
)

// Prueba de punta a punta contra una base de datos de verdad: velas → cruce →
// fila en alert_events → mensaje a Telegram. La máquina de estados ya está
// probada aparte; esto comprueba el SQL, la transacción y el drenaje de la
// cola, que es donde se rompen las cosas.
//
// Corre solo con DATABASE_URL apuntando a una base de datos de TEST:
//
//	DATABASE_URL=postgres://btc:btc@127.0.0.1:15434/btc_test go test ./internal/alerts -run E2E
//
// Usa un símbolo propio (TESTALERT) para no pisar los datos de las suites de
// frontend, que comparten esa base de datos.
const symbolPrueba = "TESTALERT"

func abrir(t *testing.T) *store.PG {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("sin DATABASE_URL: prueba de punta a punta contra la BD de test")
	}
	if !strings.HasSuffix(nombreBD(url), "_test") {
		t.Fatalf("me niego a correr contra %q: solo bases de datos de test", nombreBD(url))
	}
	pg, err := store.OpenPG(context.Background(), url)
	if err != nil {
		t.Fatalf("conexión: %v", err)
	}
	t.Cleanup(func() { pg.Pool.Close() })
	return pg
}

func nombreBD(dbURL string) string {
	u, err := url.Parse(dbURL)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Path, "/")
}

// telegramFalso devuelve un servidor que apunta lo que le mandan.
type telegramFalso struct {
	mu       sync.Mutex
	mensajes []string
	srv      *httptest.Server
}

func nuevoTelegram() *telegramFalso {
	f := &telegramFalso{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(b))
		f.mu.Lock()
		f.mensajes = append(f.mensajes, vals.Get("text"))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	return f
}

func (f *telegramFalso) recibidos() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.mensajes...)
}

func limpiar(t *testing.T, pg *store.PG) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`DELETE FROM alert_events WHERE symbol=$1`,
		`DELETE FROM alerts WHERE symbol=$1`,
		`DELETE FROM alert_engine WHERE symbol=$1`,
		`DELETE FROM candles_1s WHERE symbol=$1`,
	} {
		if _, err := pg.Pool.Exec(ctx, q, symbolPrueba); err != nil {
			t.Fatalf("limpiar: %v", err)
		}
	}
}

// meterVelas inserta velas de 1s consecutivas con el precio dado (en unidades
// de dólar), la última terminando en `fin`.
func meterVelas(t *testing.T, pg *store.PG, fin time.Time, precios ...float64) {
	t.Helper()
	ctx := context.Background()
	n := len(precios)
	for i, p := range precios {
		ts := fin.Add(time.Duration(-(n - 1 - i)) * time.Second)
		v := int64(p * 1e8)
		if _, err := pg.Pool.Exec(ctx, `
			INSERT INTO candles_1s (ts, symbol, open, high, low, close, volume, buy_volume,
			  trade_count, agg_count, first_agg_id, last_agg_id, quality)
			VALUES ($1,$2,$3,$3,$3,$3,1,1,1,1,1,1,'realtime')
			ON CONFLICT (symbol, ts) DO UPDATE SET high=EXCLUDED.high, low=EXCLUDED.low, close=EXCLUDED.close`,
			ts.UTC().Truncate(time.Second), symbolPrueba, v); err != nil {
			t.Fatalf("meter vela: %v", err)
		}
	}
}

// marcaEn coloca la marca de agua del motor: el supuesto de estos tests es
// que el motor YA venía corriendo. Al estrenarlo de cero la marca se siembra
// en el presente a propósito —nadie quiere que al arrancar le lleguen los
// cruces de la semana pasada—, y entonces no habría nada que evaluar.
func marcaEn(t *testing.T, pg *store.PG, ts time.Time) {
	t.Helper()
	if _, err := pg.Pool.Exec(context.Background(), `
		INSERT INTO alert_engine (symbol, last_ts) VALUES ($1, $2)
		ON CONFLICT (symbol) DO UPDATE SET last_ts = EXCLUDED.last_ts`,
		symbolPrueba, ts.UTC().Truncate(time.Second)); err != nil {
		t.Fatalf("marca: %v", err)
	}
}

func crearAlerta(t *testing.T, pg *store.PG, nivel float64, dir, modo string) string {
	t.Helper()
	var id string
	if err := pg.Pool.QueryRow(context.Background(), `
		INSERT INTO alerts (id, symbol, level, direction, mode, note, cooldown_sec)
		VALUES (gen_random_uuid(), $1, $2, $3, $4, 'prueba', 0) RETURNING id::text`,
		symbolPrueba, int64(nivel*1e8), dir, modo).Scan(&id); err != nil {
		t.Fatalf("crear alerta: %v", err)
	}
	return id
}

func TestE2ECruceAvisaPorTelegram(t *testing.T) {
	pg := abrir(t)
	limpiar(t, pg)
	tg := nuevoTelegram()
	defer tg.srv.Close()

	fin := time.Now().UTC().Truncate(time.Second)
	// Cinco segundos por debajo del nivel, luego el cruce, y uno más para que
	// el segundo del cruce quede CERRADO (la ventana nunca evalúa el último).
	meterVelas(t, pg, fin, 69900, 69910, 69920, 69930, 70050, 70060)
	marcaEn(t, pg, fin.Add(-6*time.Second))
	crearAlerta(t, pg, 70000, "any", "once")

	m := &Motor{PG: pg, Symbol: symbolPrueba,
		TG: &Telegram{Token: "1:x", ChatID: "9", Base: tg.srv.URL, HTTP: tg.srv.Client()}}

	// Primera pasada: siembra el lado (debajo) y cruza.
	if err := m.Paso(context.Background()); err != nil {
		t.Fatalf("paso: %v", err)
	}
	if _, err := m.Drenar(context.Background()); err != nil {
		t.Fatalf("drenar: %v", err)
	}

	msgs := tg.recibidos()
	if len(msgs) != 1 {
		t.Fatalf("esperaba UN mensaje, llegaron %d: %v", len(msgs), msgs)
	}
	for _, quiero := range []string{symbolPrueba, "70.000,00", "al alza"} {
		if !strings.Contains(msgs[0], quiero) {
			t.Errorf("el mensaje no lleva %q:\n%s", quiero, msgs[0])
		}
	}

	// El evento queda marcado como enviado y la alerta, cumplida.
	var entrega, estado string
	if err := pg.Pool.QueryRow(context.Background(),
		`SELECT delivery FROM alert_events WHERE symbol=$1 ORDER BY id DESC LIMIT 1`,
		symbolPrueba).Scan(&entrega); err != nil {
		t.Fatalf("evento: %v", err)
	}
	if entrega != "sent" {
		t.Errorf("entrega=%q", entrega)
	}
	if err := pg.Pool.QueryRow(context.Background(),
		`SELECT status FROM alerts WHERE symbol=$1`, symbolPrueba).Scan(&estado); err != nil {
		t.Fatalf("alerta: %v", err)
	}
	if estado != "done" {
		t.Errorf("una alerta 'once' debería quedar 'done', quedó %q", estado)
	}

	// Segunda pasada sin velas nuevas: ni un mensaje más.
	if err := m.Paso(context.Background()); err != nil {
		t.Fatalf("segundo paso: %v", err)
	}
	if _, err := m.Drenar(context.Background()); err != nil {
		t.Fatalf("segundo drenaje: %v", err)
	}
	if n := len(tg.recibidos()); n != 1 {
		t.Fatalf("ha vuelto a avisar: %d mensajes", n)
	}
}

// Reanudar tras una caída no puede repetir un aviso ya dado ni perder el
// cruce que estaba a medias.
func TestE2EReanudarNoRepiteNiPierde(t *testing.T) {
	pg := abrir(t)
	limpiar(t, pg)
	tg := nuevoTelegram()
	defer tg.srv.Close()

	fin := time.Now().UTC().Truncate(time.Second)
	meterVelas(t, pg, fin, 69900, 69950, 69980, 70100, 70120)
	marcaEn(t, pg, fin.Add(-5*time.Second))
	crearAlerta(t, pg, 70000, "up", "recurring")

	nuevo := func() *Motor {
		return &Motor{PG: pg, Symbol: symbolPrueba,
			TG: &Telegram{Token: "1:x", ChatID: "9", Base: tg.srv.URL, HTTP: tg.srv.Client()}}
	}
	ctx := context.Background()
	m1 := nuevo()
	if err := m1.Paso(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m1.Drenar(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(tg.recibidos()); n != 1 {
		t.Fatalf("el cruce debería avisar una vez, avisó %d", n)
	}

	// "Se cae" y arranca otro motor sobre la misma base de datos.
	m2 := nuevo()
	if err := m2.Paso(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Drenar(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(tg.recibidos()); n != 1 {
		t.Fatalf("al reanudar ha repetido el aviso: %d mensajes", n)
	}

	// Y un cruce nuevo sí avisa: baja fuera de la banda y vuelve a subir.
	meterVelas(t, pg, fin.Add(6*time.Second), 69800, 69700, 69750, 70200, 70210)
	if err := m2.Paso(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Drenar(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(tg.recibidos()); n != 2 {
		t.Fatalf("el segundo cruce debería avisar: %d mensajes", n)
	}
}

// Sin Telegram configurado el motor NO se para: evalúa, registra y marca el
// evento para que se vea en el panel.
func TestE2ESinTelegramSigueRegistrando(t *testing.T) {
	pg := abrir(t)
	limpiar(t, pg)

	fin := time.Now().UTC().Truncate(time.Second)
	meterVelas(t, pg, fin, 69900, 69950, 70100, 70110)
	marcaEn(t, pg, fin.Add(-4*time.Second))
	crearAlerta(t, pg, 70000, "any", "once")

	m := &Motor{PG: pg, Symbol: symbolPrueba, TG: &Telegram{}}
	ctx := context.Background()
	if err := m.Paso(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Drenar(ctx); err != nil {
		t.Fatal(err)
	}
	var entrega, detalle string
	if err := pg.Pool.QueryRow(ctx,
		`SELECT delivery, detail FROM alert_events WHERE symbol=$1 ORDER BY id DESC LIMIT 1`,
		symbolPrueba).Scan(&entrega, &detalle); err != nil {
		t.Fatalf("el cruce tiene que quedar registrado igualmente: %v", err)
	}
	if entrega != "skipped" || !strings.Contains(detalle, "telegram") {
		t.Fatalf("entrega=%q detalle=%q", entrega, detalle)
	}
}

// Un hueco largo (el motor parado un día) no puede soltar de golpe los cruces
// de ayer: se salta y queda constancia.
func TestE2EHuecoLargoNoReproduce(t *testing.T) {
	pg := abrir(t)
	limpiar(t, pg)
	tg := nuevoTelegram()
	defer tg.srv.Close()

	fin := time.Now().UTC().Truncate(time.Second)
	meterVelas(t, pg, fin, 69900, 70100, 70110)
	crearAlerta(t, pg, 70000, "any", "recurring")
	// Marca de agua de hace una semana.
	if _, err := pg.Pool.Exec(context.Background(), `
		INSERT INTO alert_engine (symbol, last_ts) VALUES ($1, now() - interval '7 days')
		ON CONFLICT (symbol) DO UPDATE SET last_ts = EXCLUDED.last_ts`, symbolPrueba); err != nil {
		t.Fatal(err)
	}

	m := &Motor{PG: pg, Symbol: symbolPrueba, ReplayMax: 2 * time.Hour,
		TG: &Telegram{Token: "1:x", ChatID: "9", Base: tg.srv.URL, HTTP: tg.srv.Client()}}
	ctx := context.Background()
	if err := m.Paso(ctx); err != nil {
		t.Fatal(err)
	}
	var sistema int
	if err := pg.Pool.QueryRow(ctx,
		`SELECT count(*) FROM alert_events WHERE symbol=$1 AND kind='system'`,
		symbolPrueba).Scan(&sistema); err != nil {
		t.Fatal(err)
	}
	if sistema != 1 {
		t.Fatalf("debería quedar constancia del hueco saltado, hay %d eventos de sistema", sistema)
	}
	var marca time.Time
	if err := pg.Pool.QueryRow(ctx,
		`SELECT last_ts FROM alert_engine WHERE symbol=$1`, symbolPrueba).Scan(&marca); err != nil {
		t.Fatal(err)
	}
	if time.Since(marca) > time.Hour {
		t.Fatalf("la marca debería haber saltado al presente, está en %v", marca)
	}
	fmt.Fprintln(io.Discard, sistema)
}
