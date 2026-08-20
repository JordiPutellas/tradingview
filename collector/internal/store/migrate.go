package store

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed all:migrations
var migrationsFS embed.FS

// Migrate applies pending SQL migrations in filename order. Statements run
// one by one in autocommit (no transaction): los continuous aggregates de
// Timescale no pueden crearse dentro de una transacción. Por eso cada
// sentencia es idempotente (IF NOT EXISTS / if_not_exists) y reejecutar un
// fichero a medio aplicar es seguro.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
			version INT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("migrate: bootstrap: %w", err)
	}
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migrate: bad migration filename %q", name)
		}
		var applied bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		slog.Info("applying migration", "file", name)
		for _, stmt := range splitStatements(string(body)) {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migrate: %s: %w\nstatement:\n%s", name, err, stmt)
			}
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
			return err
		}
	}
	return nil
}

// splitStatements separa por ';' a final de línea, respetando cuerpos de
// función delimitados por dollar-quoting ($$...$$), donde hay ';' internos.
func splitStatements(sql string) []string {
	var out []string
	var cur strings.Builder
	inDollar := false
	for _, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if !inDollar && (strings.HasPrefix(trimmed, "--") || trimmed == "") {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		if n := strings.Count(line, "$$"); n%2 == 1 {
			inDollar = !inDollar
		}
		if !inDollar && strings.HasSuffix(trimmed, ";") {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}
