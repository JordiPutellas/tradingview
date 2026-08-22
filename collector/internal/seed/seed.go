// Package seed genera un histórico sintético y REPRODUCIBLE para la base de
// datos de test (F5, bloque 1).
//
// Por qué existe: hasta F4 las suites de frontend corrían contra la base de
// datos de PRODUCCIÓN a través del túnel, y llegaron a borrar los dibujos del
// usuario. El arreglo de raíz es una base de datos aparte con datos propios;
// este paquete los fabrica.
//
// Dos reglas que no se negocian:
//   - Guard() aborta si la URL no apunta a una base de datos cuyo nombre acaba
//     en "_test". Falla CERRADO: si la URL no se entiende, no se toca nada.
//   - Las velas son deterministas (semilla fija): la misma forma en cada
//     ejecución, para que un test que falla se pueda repetir.
package seed

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"jputellas.dev/btcdash/collector/internal/store"
)

// Sufijo obligatorio en el nombre de la base de datos de test.
const Sufijo = "_test"

// Guard comprueba que la URL apunta a una base de datos de test. Es la única
// barrera entre un `seed-test` despistado y el histórico de producción, así
// que ante cualquier duda —una URL que no se puede analizar, un nombre vacío—
// devuelve error en vez de seguir.
func Guard(dbURL string) error {
	u, err := url.Parse(dbURL)
	if err != nil {
		return fmt.Errorf("DATABASE_URL ilegible: %w", err)
	}
	// Solo se entiende la forma URL. Un DSN "host=... dbname=..." se rechaza
	// aunque parezca de test: url.Parse se lo traga entero como ruta y el
	// nombre de la base de datos podría ser cualquier otra cosa.
	if u.Scheme != "postgres" && u.Scheme != "postgresql" {
		return fmt.Errorf("me niego: DATABASE_URL no es una URL postgres:// "+
			"y no puedo saber a qué base de datos apunta (%q)", dbURL)
	}
	nombre := strings.TrimPrefix(u.Path, "/")
	if nombre == "" || !strings.HasSuffix(nombre, Sufijo) {
		return fmt.Errorf("me niego: la base de datos %q no acaba en %q; "+
			"esto solo corre contra la base de datos de test", nombre, Sufijo)
	}
	return nil
}

// Opts describe la ventana de datos a fabricar.
type Opts struct {
	Symbol  string
	Days    int     // días de velas de 1m hacia atrás
	Hours1s int     // horas finales con velas de 1s (y su rollup a 1m)
	Start   float64 // precio de partida
}

func (o *Opts) defaults() {
	if o.Symbol == "" {
		o.Symbol = "BTCUSDT"
	}
	if o.Days <= 0 {
		o.Days = 400
	}
	if o.Hours1s <= 0 {
		o.Hours1s = 6
	}
	if o.Start <= 0 {
		o.Start = 20000
	}
}

// paseo genera una serie de precios reproducible. No pretende parecerse al
// mercado: solo tiene que subir y bajar de forma creíble (hay comprobaciones
// que cuentan píxeles de vela alcista y bajista) y no irse a cero.
type paseo struct {
	rng   *rand.Rand
	p     float64
	i     int
	deriv float64
}

func nuevoPaseo(start float64) *paseo {
	return &paseo{rng: rand.New(rand.NewPCG(0xB7C0DA5, 0x1F5EED)), p: start}
}

// vela avanza n sub-movimientos y devuelve OHLC coherente (high es el máximo
// de verdad, low el mínimo) y un volumen positivo.
func (w *paseo) vela(sub int) (o, h, l, c, v float64) {
	o = w.p
	h, l = o, o
	for k := 0; k < sub; k++ {
		w.i++
		// Ciclo lento + ruido + una deriva positiva mínima: tramos alcistas y
		// bajistas largos sobre una tendencia de fondo, para que la ventana
		// entera vaya de ~20k a ~80k. Así la escala de precio tiene algo que
		// hacer y el histórico lejano no vale lo mismo que el reciente.
		w.deriv = 4.0e-7 + 0.00004*math.Sin(float64(w.i)/2000)
		w.p *= 1 + w.deriv + (w.rng.Float64()-0.5)*0.0009
		if w.p < 5000 {
			w.p = 5000
		}
		h = math.Max(h, w.p)
		l = math.Min(l, w.p)
	}
	c = w.p
	v = 0.5 + w.rng.Float64()*40
	return
}

const e8 = 1e8

func fix(x float64) int64 { return int64(math.Round(x * e8)) }

// fila es una vela lista para COPY, en el orden de columnas de las tablas.
type fila struct {
	ts                                time.Time
	o, h, l, c, v, bv                 int64
	trades, aggs, firstAgg, lastAggID int64
	quality                           string
}

func (f fila) valores(symbol string) []any {
	return []any{f.ts, symbol, f.o, f.h, f.l, f.c, f.v, f.bv,
		f.trades, f.aggs, f.firstAgg, f.lastAggID, f.quality}
}

var columnas = []string{"ts", "symbol", "open", "high", "low", "close", "volume",
	"buy_volume", "trade_count", "agg_count", "first_agg_id", "last_agg_id", "quality"}

// Run vacía las tablas de velas y las repuebla. Es idempotente: dos
// ejecuciones seguidas dejan la misma base de datos (salvo el instante final,
// que se mueve con el reloj).
// Reset deja la base de datos como recién creada. Hace falta borrar el
// ESQUEMA entero, no vaciar tablas: lo materializado de una CAgg vive en otra
// hypertable que TRUNCATE no toca, y refrescar con los límites a NULL tampoco
// lo borra (comprobado: tras resembrar quedaba una vela de 1h de la ventana
// anterior). Con velas de 1d y 1h más viejas que candles_1m, los tests —que
// preguntan por el primer día en tf=1D— apuntarían a un tramo vacío.
func Reset(ctx context.Context, pg *store.PG) error {
	_, err := pg.Pool.Exec(ctx, `DROP SCHEMA public CASCADE;
CREATE SCHEMA public;
GRANT ALL ON SCHEMA public TO CURRENT_USER;
GRANT ALL ON SCHEMA public TO public`)
	if err != nil {
		return fmt.Errorf("reiniciar esquema: %w", err)
	}
	// La extensión timescaledb vive en public y se va con el esquema. El
	// backend que la tenía cargada se niega a recrearla —"has already been
	// loaded with another version"— así que hay que forzar conexiones nuevas
	// antes de migrar. Sin esto la siembra falla una de cada dos veces, según
	// qué conexión del pool toque.
	pg.Pool.Reset()
	return nil
}

func Run(ctx context.Context, pg *store.PG, o Opts) error {
	o.defaults()
	now := time.Now().UTC().Truncate(time.Minute)
	inicio1m := now.Add(-time.Duration(o.Days) * 24 * time.Hour)
	inicio1s := now.Add(-time.Duration(o.Hours1s) * time.Hour)

	w := nuevoPaseo(o.Start)
	var aggID int64 = 1

	// 1) velas de 1m sintéticas hasta donde empieza el detalle de 1s
	var filas1m []fila
	for ts := inicio1m; ts.Before(inicio1s); ts = ts.Add(time.Minute) {
		op, hi, lo, cl, vol := w.vela(6)
		filas1m = append(filas1m, fila{
			ts: ts, o: fix(op), h: fix(hi), l: fix(lo), c: fix(cl),
			v: fix(vol), bv: fix(vol * 0.5), trades: 120, aggs: 60,
			firstAgg: aggID, lastAggID: aggID + 59, quality: "derived",
		})
		aggID += 60
	}
	if err := copiar(ctx, pg, "candles_1m", o.Symbol, filas1m); err != nil {
		return err
	}

	// 2) el tramo final en 1s, y su rollup a 1m: así las dos tablas cuadran
	//    donde los tests miran de cerca (streaming, timeframes de segundos).
	var filas1s []fila
	for ts := inicio1s; ts.Before(now); ts = ts.Add(time.Second) {
		op, hi, lo, cl, vol := w.vela(2)
		filas1s = append(filas1s, fila{
			ts: ts, o: fix(op), h: fix(hi), l: fix(lo), c: fix(cl),
			v: fix(vol / 60), bv: fix(vol / 120), trades: 3, aggs: 2,
			firstAgg: aggID, lastAggID: aggID + 1, quality: store.QualityRealtime,
		})
		aggID += 2
	}
	if err := copiar(ctx, pg, "candles_1s", o.Symbol, filas1s); err != nil {
		return err
	}
	if err := pg.RollupRange(ctx, inicio1s, now); err != nil {
		return fmt.Errorf("rollup 1m: %w", err)
	}

	// 3) los timeframes de 3m en adelante son CAggs: sin refrescar sirven
	//    vacío por debajo de la marca de agua (trampa 13 del README).
	if err := pg.RefreshCAggs(ctx, inicio1m, now); err != nil {
		return fmt.Errorf("refrescar caggs: %w", err)
	}
	return nil
}

func copiar(ctx context.Context, pg *store.PG, tabla, symbol string, fs []fila) error {
	if len(fs) == 0 {
		return nil
	}
	src := pgx.CopyFromSlice(len(fs), func(i int) ([]any, error) {
		return fs[i].valores(symbol), nil
	})
	n, err := pg.Pool.CopyFrom(ctx, pgx.Identifier{tabla}, columnas, src)
	if err != nil {
		return fmt.Errorf("copy %s: %w", tabla, err)
	}
	if int(n) != len(fs) {
		return fmt.Errorf("copy %s: %d filas de %d", tabla, n, len(fs))
	}
	return nil
}

// Live simula al colector en la base de datos de test: una vela de 1s por
// segundo, con su NOTIFY, para que el streaming del frontend tenga algo que
// mostrar. Cada minuto refresca el rollup a 1m.
//
// No es el colector de verdad ni lo pretende: no hay reconciliación, ni
// huecos, ni WebSocket de Binance.
func Live(ctx context.Context, pg *store.PG, symbol string) error {
	ultima, err := pg.LastCandle(ctx, symbol)
	if err != nil {
		return err
	}
	precio := 70000.0
	var aggID int64 = 1
	if ultima != nil {
		precio = float64(ultima.Close) / e8
		aggID = ultima.LastAggID + 1
	}
	w := nuevoPaseo(precio)
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var ultimoRollup time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		ts := time.Now().UTC().Truncate(time.Second)
		op, hi, lo, cl, vol := w.vela(2)
		c := store.StoredCandle{Symbol: symbol, Quality: store.QualityRealtime}
		c.TsSec = ts.Unix()
		c.Open, c.High, c.Low, c.Close = fix(op), fix(hi), fix(lo), fix(cl)
		c.Volume, c.BuyVolume = fix(vol/60), fix(vol/120)
		c.TradeCount, c.AggCount = 3, 2
		c.FirstAggID, c.LastAggID = aggID, aggID+1
		aggID += 2
		if err := pg.UpsertCandles(ctx, []store.StoredCandle{c}); err != nil {
			return err
		}
		if err := pg.NotifyCandle(ctx, c); err != nil {
			return err
		}
		if ts.Sub(ultimoRollup) >= time.Minute {
			// ventana holgada: el rollup es idempotente y barato aquí
			if err := pg.RollupRange(ctx, ts.Add(-3*time.Minute), ts.Add(time.Minute)); err != nil {
				return err
			}
			ultimoRollup = ts
		}
	}
}
