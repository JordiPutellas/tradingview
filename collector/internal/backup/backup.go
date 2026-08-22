// Package backup copia lo que no se puede volver a bajar de internet (F5,
// bloque 2. RF-6.4, pendiente desde F1a).
//
// La idea de fondo: esta base de datos es, en su mayor parte, una CACHÉ DE
// BINANCE. Las 61 millones de velas de 1s y los 3,6 millones de 1m se pueden
// volver a descargar con `backfill` y `backfill-1m`, y las once continuous
// aggregates se rematerializan con `refresh-caggs` en minutos. Lo que NO
// existe en ningún otro sitio son los dibujos del usuario, las alertas, el
// registro de huecos y el progreso de los jobs: kilobytes.
//
// De ahí las capas:
//
//	estado  cada 6 h  ~40 KB   irreemplazable: dibujos, alertas, huecos, progreso
//	1m      semanal   ~95 MB   espinazo histórico: de él salen 22 de los 24 TFs
//	1s      diaria    ~3 MB    un día UTC, el de anteayer (ya corregido por t1)
//
// Y lo que a propósito NO se copia: las CAggs (derivadas) y el grueso de
// candles_1s antiguo (re-descargable). Queda escrito para que nadie lo
// "arregle" dentro de seis meses.
//
// Formato: CSV comprimido, no pg_dump. Motivos: la imagen no lleva cliente de
// postgres; un dump de una hypertable NO incluye los datos (los chunks viven
// en otro esquema, comprobado: `pg_dump -t candles_1m` son 881 bytes); y el
// esquema se recrea con `collector migrate`, que está en git y es idempotente.
// Restaurar es un COPY FROM, que entiende cualquier PostgreSQL.
package backup

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jputellas.dev/btcdash/collector/internal/store"
)

// TablasEstado: lo irreemplazable. Las que no existan todavía se saltan, para
// que el backup siga funcionando entre migraciones.
var TablasEstado = []string{
	"drawings", "alerts", "alert_events", "alert_engine",
	"data_gaps", "backfill_progress", "job_progress", "schema_migrations",
}

// Fichero es una copia ya escrita en disco.
type Fichero struct {
	Ruta   string `json:"-"`
	Clave  string `json:"clave"` // ruta relativa: también es la clave en el bucket
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	// Filas y Tabla se cuentan MIENTRAS se copia, no preguntando después a la
	// base de datos: el colector sigue escribiendo y entre el COPY y el
	// count(*) entra otra vela. La primera prueba de restauración cazó
	// justamente eso — 3.657.397 filas restauradas contra 3.657.398 en el
	// manifiesto— y un backup cuyo manifiesto no cuadra nunca es un backup en
	// el que se pueda confiar.
	Filas int64  `json:"filas"`
	Tabla string `json:"tabla,omitempty"`
}

// Estado vuelca las tablas pequeñas. Devuelve un fichero por tabla existente.
func Estado(ctx context.Context, pg *store.PG, dir string, sello time.Time) ([]Fichero, error) {
	destino := filepath.Join(dir, "estado", sello.UTC().Format("2006-01-02T1504Z"))
	if err := os.MkdirAll(destino, 0o755); err != nil {
		return nil, err
	}
	var out []Fichero
	for _, t := range TablasEstado {
		hay, err := existe(ctx, pg, t)
		if err != nil {
			return nil, err
		}
		if !hay {
			continue
		}
		f, err := copiar(ctx, pg, fmt.Sprintf(`COPY (SELECT * FROM %s) TO STDOUT (FORMAT csv, HEADER)`, t),
			filepath.Join(destino, t+".csv.gz"), dir)
		if err != nil {
			return nil, err
		}
		f.Tabla = t
		out = append(out, f)
	}
	return out, nil
}

// Velas1m vuelca candles_1m entera. ~95 MB comprimidos, medio minuto.
func Velas1m(ctx context.Context, pg *store.PG, dir string, sello time.Time) (Fichero, error) {
	destino := filepath.Join(dir, "1m")
	if err := os.MkdirAll(destino, 0o755); err != nil {
		return Fichero{}, err
	}
	f, err := copiar(ctx, pg, `COPY (SELECT * FROM candles_1m ORDER BY ts) TO STDOUT (FORMAT csv, HEADER)`,
		filepath.Join(destino, "candles_1m-"+sello.UTC().Format("2006-01-02")+".csv.gz"), dir)
	f.Tabla = "candles_1m"
	return f, err
}

// Velas1sDia vuelca un día UTC de candles_1s. Se copia el de ANTEAYER, no el
// de ayer: el cron de t1 corrige el día anterior a las 09:40 UTC, así que con
// un día de margen el archivo sale ya con quality='exact_t1' y no hay que
// volver a copiarlo nunca.
func Velas1sDia(ctx context.Context, pg *store.PG, dir string, dia time.Time) (Fichero, error) {
	destino := filepath.Join(dir, "1s")
	if err := os.MkdirAll(destino, 0o755); err != nil {
		return Fichero{}, err
	}
	d := dia.UTC().Truncate(24 * time.Hour)
	sql := fmt.Sprintf(`COPY (SELECT * FROM candles_1s
	  WHERE ts >= '%s'::timestamptz AND ts < '%s'::timestamptz ORDER BY ts)
	  TO STDOUT (FORMAT csv, HEADER)`,
		d.Format(time.RFC3339), d.Add(24*time.Hour).Format(time.RFC3339))
	f, err := copiar(ctx, pg, sql, filepath.Join(destino, d.Format("2006-01-02")+".csv.gz"), dir)
	f.Tabla = "candles_1s@" + d.Format("2006-01-02")
	return f, err
}

// Manifiesto: qué debería haber ahí dentro. Sirve para dos cosas — verificar
// una restauración sin comparar byte a byte, y saber qué falta si un día hay
// que reconstruir desde Binance.
type Manifiesto struct {
	Generado string          `json:"generado"`
	Symbol   string          `json:"symbol"`
	Tablas   map[string]Info `json:"tablas"`
	Ficheros []Fichero       `json:"ficheros"`
}

type Info struct {
	Filas   int64  `json:"filas"`
	Primera string `json:"primera,omitempty"`
	Ultima  string `json:"ultima,omitempty"`
}

func HacerManifiesto(ctx context.Context, pg *store.PG, symbol string, fs []Fichero) (Manifiesto, error) {
	m := Manifiesto{
		Generado: time.Now().UTC().Format(time.RFC3339),
		Symbol:   symbol,
		Tablas:   map[string]Info{},
		Ficheros: fs,
	}
	conRango := []string{"candles_1s", "candles_1m"}
	for _, ca := range store.CAggs {
		conRango = append(conRango, ca.View)
	}
	for _, t := range conRango {
		hay, err := existe(ctx, pg, t)
		if err != nil || !hay {
			continue
		}
		var i Info
		var pri, ult *time.Time
		if err := pg.Pool.QueryRow(ctx,
			fmt.Sprintf(`SELECT count(*), min(ts), max(ts) FROM %s`, t)).Scan(&i.Filas, &pri, &ult); err != nil {
			return m, fmt.Errorf("manifiesto %s: %w", t, err)
		}
		if pri != nil {
			i.Primera = pri.UTC().Format(time.RFC3339)
		}
		if ult != nil {
			i.Ultima = ult.UTC().Format(time.RFC3339)
		}
		m.Tablas[t] = i
	}
	for _, t := range TablasEstado {
		hay, err := existe(ctx, pg, t)
		if err != nil || !hay {
			continue
		}
		var i Info
		if err := pg.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, t)).Scan(&i.Filas); err != nil {
			return m, fmt.Errorf("manifiesto %s: %w", t, err)
		}
		m.Tablas[t] = i
	}
	// Manda lo COPIADO sobre lo que diga la base de datos ahora: es lo que se
	// va a poder restaurar. Para candles_1m la diferencia es de una vela, pero
	// una vela es la diferencia entre "verificado" y "no cuadra".
	for _, f := range fs {
		if f.Tabla == "" || strings.Contains(f.Tabla, "@") {
			continue
		}
		info := m.Tablas[f.Tabla]
		info.Filas = f.Filas
		m.Tablas[f.Tabla] = info
	}
	return m, nil
}

func (m Manifiesto) Escribir(dir string, sello time.Time) (Fichero, error) {
	destino := filepath.Join(dir, "manifiesto")
	if err := os.MkdirAll(destino, 0o755); err != nil {
		return Fichero{}, err
	}
	ruta := filepath.Join(destino, "manifiesto-"+sello.UTC().Format("2006-01-02T1504Z")+".json")
	b, err := json.MarshalIndent(m, "", " ")
	if err != nil {
		return Fichero{}, err
	}
	if err := escribirAtomico(ruta, b); err != nil {
		return Fichero{}, err
	}
	return describir(ruta, dir)
}

// Podar borra las copias viejas de una capa, dejando siempre al menos `min`.
// Devuelve lo borrado.
func Podar(dir, capa string, keep int, min int) ([]string, error) {
	d := filepath.Join(dir, capa)
	entradas, err := os.ReadDir(d)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type item struct {
		ruta string
		mod  time.Time
	}
	var items []item
	for _, e := range entradas {
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{filepath.Join(d, e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })
	var borrados []string
	limite := time.Now().Add(-time.Duration(keep) * 24 * time.Hour)
	for i, it := range items {
		if i < min || it.mod.After(limite) {
			continue
		}
		if err := os.RemoveAll(it.ruta); err != nil {
			return borrados, err
		}
		borrados = append(borrados, it.ruta)
	}
	return borrados, nil
}

// Restaurar mete un CSV comprimido en una tabla. Vacía la tabla antes: es una
// restauración, no una fusión.
func Restaurar(ctx context.Context, pg *store.PG, tabla, ruta string) (int64, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gz.Close()

	conn, err := pg.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, fmt.Sprintf(`TRUNCATE %s`, tabla)); err != nil {
		return 0, fmt.Errorf("vaciar %s: %w", tabla, err)
	}
	tag, err := conn.Conn().PgConn().CopyFrom(ctx, gz,
		fmt.Sprintf(`COPY %s FROM STDIN (FORMAT csv, HEADER)`, tabla))
	if err != nil {
		return 0, fmt.Errorf("restaurar %s: %w", tabla, err)
	}
	return tag.RowsAffected(), nil
}

// ---------- internos ----------

func existe(ctx context.Context, pg *store.PG, tabla string) (bool, error) {
	var ok bool
	err := pg.Pool.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, tabla).Scan(&ok)
	return ok, err
}

// copiar ejecuta un COPY ... TO STDOUT contra un fichero .gz. Escribe primero
// a .tmp y renombra: un backup a medias no puede parecer completo (es el modo
// de fallo clásico de los scripts con pipes sin pipefail).
func copiar(ctx context.Context, pg *store.PG, sql, ruta, raiz string) (Fichero, error) {
	tmp := ruta + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return Fichero{}, err
	}
	gz := gzip.NewWriter(f)
	cont := &contador{w: gz}
	conn, err := pg.Pool.Acquire(ctx)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return Fichero{}, err
	}
	_, err = conn.Conn().PgConn().CopyTo(ctx, cont, sql)
	conn.Release()
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return Fichero{}, fmt.Errorf("copy: %w", err)
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return Fichero{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return Fichero{}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return Fichero{}, err
	}
	if err := os.Rename(tmp, ruta); err != nil {
		return Fichero{}, err
	}
	fi, err := describir(ruta, raiz)
	if err != nil {
		return Fichero{}, err
	}
	fi.Filas = cont.lineas - 1 // la cabecera del CSV no es una fila
	if fi.Filas < 0 {
		fi.Filas = 0
	}
	return fi, nil
}

// contador cuenta líneas al vuelo mientras el COPY escribe.
type contador struct {
	w      io.Writer
	lineas int64
}

func (c *contador) Write(p []byte) (int, error) {
	for _, b := range p {
		if b == '\n' {
			c.lineas++
		}
	}
	return c.w.Write(p)
}

func describir(ruta, raiz string) (Fichero, error) {
	f, err := os.Open(ruta)
	if err != nil {
		return Fichero{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return Fichero{}, err
	}
	rel, err := filepath.Rel(raiz, ruta)
	if err != nil {
		rel = filepath.Base(ruta)
	}
	return Fichero{
		Ruta:   ruta,
		Clave:  filepath.ToSlash(rel),
		Bytes:  n,
		SHA256: hex.EncodeToString(h.Sum(nil)),
	}, nil
}

func escribirAtomico(ruta string, b []byte) error {
	tmp := ruta + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, ruta)
}

// TablaDeFichero deduce a qué tabla pertenece un fichero de la capa estado.
func TablaDeFichero(ruta string) string {
	return strings.TrimSuffix(filepath.Base(ruta), ".csv.gz")
}
