package store

import (
	"strings"
	"testing"
)

// El splitter debe respetar los ';' internos de cuerpos $$...$$ (funciones y
// procedures de la migración 002).
func TestSplitStatementsDollarQuoting(t *testing.T) {
	sql := `-- comentario
CREATE TABLE t (a INT);
CREATE OR REPLACE FUNCTION f() RETURNS void LANGUAGE sql AS $$
  INSERT INTO t VALUES (1);
  INSERT INTO t VALUES (2);
$$;
SELECT add_job('f', INTERVAL '1 minute')
WHERE NOT EXISTS (SELECT 1);
`
	got := splitStatements(sql)
	if len(got) != 3 {
		t.Fatalf("expected 3 statements, got %d: %q", len(got), got)
	}
	if !strings.Contains(got[1], "VALUES (2);") || !strings.Contains(got[1], "$$;") {
		t.Errorf("function body split incorrectly: %q", got[1])
	}
	if !strings.HasPrefix(strings.TrimSpace(got[2]), "SELECT add_job") {
		t.Errorf("third statement wrong: %q", got[2])
	}
}

// Todas las migraciones embebidas deben partirse sin dejar fragmentos sueltos:
// cada sentencia resultante termina en ';' y los $$ quedan emparejados.
func TestEmbeddedMigrationsSplitClean(t *testing.T) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for i, stmt := range splitStatements(string(body)) {
			if !strings.HasSuffix(strings.TrimSpace(stmt), ";") {
				t.Errorf("%s: statement %d does not end in ';': %.80q", e.Name(), i, stmt)
			}
			if strings.Count(stmt, "$$")%2 != 0 {
				t.Errorf("%s: statement %d has unbalanced $$: %.80q", e.Name(), i, stmt)
			}
		}
	}
}
