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
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"jputellas.dev/btcdash/collector/internal/api"
	"jputellas.dev/btcdash/collector/internal/backfill"
	"jputellas.dev/btcdash/collector/internal/backup"
	"jputellas.dev/btcdash/collector/internal/binance"
	"jputellas.dev/btcdash/collector/internal/collect"
	"jputellas.dev/btcdash/collector/internal/config"
	"jputellas.dev/btcdash/collector/internal/health"
	"jputellas.dev/btcdash/collector/internal/seed"
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
	case "seed-test":
		// Base de datos de TEST: histórico sintético reproducible (F5).
		// El guard va ANTES de tocar nada y aborta si la URL no acaba en
		// _test; en producción este comando no puede hacer daño.
		fs := flag.NewFlagSet("seed-test", flag.ExitOnError)
		days := fs.Int("days", 400, "días de velas de 1m")
		hours1s := fs.Int("hours-1s", 6, "horas finales con velas de 1s")
		live := fs.Bool("live", false, "no genera: emite una vela de 1s por segundo, como el colector")
		fs.Parse(args)
		if err := seed.Guard(cfg.DatabaseURL); err != nil {
			return err
		}
		if *live {
			slog.Info("feeder de test en marcha", "symbol", cfg.Symbol)
			return seed.Live(ctx, pg, cfg.Symbol)
		}
		// Borra y rehace: la base de datos de test no guarda nada que valga.
		if err := seed.Reset(ctx, pg); err != nil {
			return err
		}
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		if err := seed.Run(ctx, pg, seed.Opts{Symbol: cfg.Symbol, Days: *days, Hours1s: *hours1s}); err != nil {
			return err
		}
		cov, err := api.Coverage(ctx, pg.Pool, cfg.Symbol)
		if err != nil {
			return err
		}
		for _, c := range cov {
			fmt.Printf("%-4s %-11s %s → %s\n", c.TF, c.Src, tstamp(c.First), tstamp(c.Last))
		}
		return nil
	case "backup":
		// Copia por capas (F5, bloque 2). Diseñado para correr por cron:
		// escribe en local, sube a S3 si hay credenciales y avisa a
		// healthchecks.io del resultado — un backup que falla en silencio no
		// es un backup.
		fs := flag.NewFlagSet("backup", flag.ExitOnError)
		dir := fs.String("dir", "/backups", "directorio de destino")
		capas := fs.String("capas", "estado", "capas: estado,1m,1s (coma)")
		keepEstado := fs.Int("keep-estado", 30, "días de copias de estado")
		keep1m := fs.Int("keep-1m", 90, "días de copias de candles_1m")
		keep1s := fs.Int("keep-1s", 400, "días de días de candles_1s")
		fs.Parse(args)
		return hacerBackup(ctx, pg, cfg.Symbol, *dir, strings.Split(*capas, ","),
			map[string]int{"estado": *keepEstado, "1m": *keep1m, "1s": *keep1s})
	case "restore":
		// Restauración. Contra una base de datos que no sea de test hay que
		// decirlo a propósito con -force: restaurar encima de producción por
		// error es peor que no tener backup.
		fs := flag.NewFlagSet("restore", flag.ExitOnError)
		dir := fs.String("dir", "", "directorio de una copia de estado (o fichero .csv.gz suelto)")
		tabla := fs.String("tabla", "", "tabla destino si -dir es un fichero suelto")
		force := fs.Bool("force", false, "permitir restaurar sobre una base de datos que no sea de test")
		fs.Parse(args)
		if *dir == "" {
			return fmt.Errorf("-dir es obligatorio")
		}
		if err := seed.Guard(cfg.DatabaseURL); err != nil && !*force {
			return fmt.Errorf("%w (usa -force si de verdad quieres restaurar aquí)", err)
		}
		// El esquema se recrea con las migraciones, que están en git: el
		// backup no lleva DDL a propósito (ver internal/backup).
		if err := store.Migrate(ctx, pg.Pool); err != nil {
			return err
		}
		return restaurar(ctx, pg, *dir, *tabla)
	case "verify-restore":
		// "Un backup sin restauración verificada no es un backup": compara lo
		// que hay en ESTA base de datos contra el manifiesto de la copia.
		fs := flag.NewFlagSet("verify-restore", flag.ExitOnError)
		man := fs.String("manifiesto", "", "fichero manifiesto-*.json de la copia")
		tablas := fs.String("tablas", strings.Join(backup.TablasEstado, ","), "tablas a comprobar")
		fs.Parse(args)
		if *man == "" {
			return fmt.Errorf("-manifiesto es obligatorio")
		}
		return verificarRestauracion(ctx, pg, *man, strings.Split(*tablas, ","))
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

// hacerBackup ejecuta las capas pedidas, poda lo viejo, sube a S3 si hay
// credenciales y avisa a healthchecks.io. Devuelve error si algo falla, y en
// ese caso el ping va a /fail: el silencio no cuenta como éxito.
func hacerBackup(ctx context.Context, pg *store.PG, symbol, dir string, capas []string, keep map[string]int) error {
	hc := os.Getenv("BACKUP_HEALTHCHECK_URL")
	ping(ctx, hc, "/start")
	err := backupCapas(ctx, pg, symbol, dir, capas, keep)
	if err != nil {
		ping(ctx, hc, "/fail")
		return err
	}
	ping(ctx, hc, "")
	return nil
}

func backupCapas(ctx context.Context, pg *store.PG, symbol, dir string, capas []string, keep map[string]int) error {
	sello := time.Now().UTC()
	var fs []backup.Fichero
	for _, c := range capas {
		switch strings.TrimSpace(c) {
		case "estado":
			f, err := backup.Estado(ctx, pg, dir, sello)
			if err != nil {
				return err
			}
			fs = append(fs, f...)
		case "1m":
			f, err := backup.Velas1m(ctx, pg, dir, sello)
			if err != nil {
				return err
			}
			fs = append(fs, f)
		case "1s":
			// Anteayer: el cron de t1 corrige el día anterior a las 09:40 UTC,
			// así que con un día de margen el fichero sale ya exacto y no hay
			// que volver a copiarlo nunca.
			dia := sello.AddDate(0, 0, -2)
			f, err := backup.Velas1sDia(ctx, pg, dir, dia)
			if err != nil {
				return err
			}
			fs = append(fs, f)
		case "":
		default:
			return fmt.Errorf("capa desconocida: %q", c)
		}
	}
	man, err := backup.HacerManifiesto(ctx, pg, symbol, fs)
	if err != nil {
		return err
	}
	fman, err := man.Escribir(dir, sello)
	if err != nil {
		return err
	}
	fs = append(fs, fman)
	for _, f := range fs {
		slog.Info("copia escrita", "clave", f.Clave, "bytes", f.Bytes)
	}

	// Poda local ANTES de subir: si el disco está justo, lo que sobra es lo
	// viejo, no la copia recién hecha.
	for capa, dias := range keep {
		borrados, err := backup.Podar(dir, capa, dias, 3)
		if err != nil {
			return err
		}
		if len(borrados) > 0 {
			slog.Info("podado local", "capa", capa, "borrados", len(borrados))
		}
	}
	backup.Podar(dir, "manifiesto", keep["estado"], 3)

	s3, ok := backup.S3DesdeEnv()
	if !ok {
		slog.Warn("sin credenciales S3: la copia se queda EN EL MISMO SERVIDOR que la base de datos " +
			"(configura BACKUP_S3_* en .env; ver RUNBOOK)")
		return nil
	}
	if err := s3.Subir(ctx, fs); err != nil {
		return err
	}
	slog.Info("subido a S3", "bucket", s3.Bucket, "ficheros", len(fs))
	for capa, dias := range keep {
		borrados, err := s3.PodarRemoto(ctx, capa, dias, 3)
		if err != nil {
			return err
		}
		if len(borrados) > 0 {
			slog.Info("podado remoto", "capa", capa, "borrados", len(borrados))
		}
	}
	return nil
}

// restaurar mete de vuelta una copia de estado (un directorio con un csv.gz
// por tabla) o un fichero suelto en la tabla indicada.
func restaurar(ctx context.Context, pg *store.PG, dir, tabla string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		if tabla == "" {
			tabla = backup.TablaDeFichero(dir)
		}
		n, err := backup.Restaurar(ctx, pg, tabla, dir)
		if err != nil {
			return err
		}
		slog.Info("restaurado", "tabla", tabla, "filas", n)
		return nil
	}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entradas {
		if !strings.HasSuffix(e.Name(), ".csv.gz") {
			continue
		}
		ruta := filepath.Join(dir, e.Name())
		t := backup.TablaDeFichero(ruta)
		n, err := backup.Restaurar(ctx, pg, t, ruta)
		if err != nil {
			return err
		}
		slog.Info("restaurado", "tabla", t, "filas", n)
	}
	return nil
}

// ping avisa a healthchecks.io. Best-effort y con timeout corto: el backup no
// se cae porque el avisador esté caído.
func ping(ctx context.Context, base, sufijo string) {
	if base == "" {
		return
	}
	c := &http.Client{Timeout: 10 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+sufijo, nil)
	if err != nil {
		return
	}
	if resp, err := c.Do(req); err == nil {
		resp.Body.Close()
	} else {
		slog.Warn("ping de backup fallido", "err", err)
	}
}

// verificarRestauracion compara la base de datos actual con el manifiesto de
// una copia. Falla si algo no cuadra: es la diferencia entre tener backups y
// creer que se tienen.
func verificarRestauracion(ctx context.Context, pg *store.PG, ruta string, tablas []string) error {
	b, err := os.ReadFile(ruta)
	if err != nil {
		return err
	}
	var man backup.Manifiesto
	if err := json.Unmarshal(b, &man); err != nil {
		return err
	}
	var fallos int
	for _, t := range tablas {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		esperado, ok := man.Tablas[t]
		if !ok {
			fmt.Printf("· %-20s no está en el manifiesto, se salta\n", t)
			continue
		}
		var filas int64
		if err := pg.Pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, t)).Scan(&filas); err != nil {
			fmt.Printf("✗ %-20s no se puede leer: %v\n", t, err)
			fallos++
			continue
		}
		if filas != esperado.Filas {
			fmt.Printf("✗ %-20s %d filas, el manifiesto dice %d\n", t, filas, esperado.Filas)
			fallos++
			continue
		}
		fmt.Printf("✓ %-20s %d filas\n", t, filas)
	}
	if fallos > 0 {
		return fmt.Errorf("%d tabla(s) no cuadran con el manifiesto", fallos)
	}
	fmt.Printf("restauración verificada contra %s\n", filepath.Base(ruta))
	return nil
}
