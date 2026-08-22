package seed

import "testing"

// El guard es la única barrera entre `seed-test` y el histórico de producción,
// así que se prueba a conciencia y, sobre todo, que falla CERRADO: cualquier
// URL que no se entienda tiene que abortar, no seguir adelante.
func TestGuard(t *testing.T) {
	casos := []struct {
		url  string
		pasa bool
	}{
		{"postgres://btc:btc@127.0.0.1:5433/btc_test", true},
		{"postgres://btc:btc@127.0.0.1:5433/btc_test?sslmode=disable", true},
		{"postgres://u:p@host/otra_cosa_test", true},

		{"postgres://btc:btc@127.0.0.1:5433/btc", false},             // producción
		{"postgres://btc:btc@127.0.0.1:5433/", false},                // sin nombre
		{"postgres://btc:btc@127.0.0.1:5433", false},                 // sin ruta
		{"postgres://btc:btc@127.0.0.1:5433/btc_test_backup", false}, // no acaba en _test
		{"postgres://btc:btc@127.0.0.1:5433/testbtc", false},         // el sufijo no es prefijo
		{"host=127.0.0.1 dbname=btc_test", false},                    // DSN key=value: no se sabe leer -> aborta
		{"", false},
		{"://malformada", false},
	}
	for _, c := range casos {
		err := Guard(c.url)
		if c.pasa && err != nil {
			t.Errorf("Guard(%q) debería pasar y devolvió %v", c.url, err)
		}
		if !c.pasa && err == nil {
			t.Errorf("Guard(%q) debería ABORTAR y pasó", c.url)
		}
	}
}

// El paseo tiene que ser reproducible: dos generadores con la misma semilla
// producen la misma serie. Si no, un test que falla no se puede repetir.
func TestPaseoReproducible(t *testing.T) {
	a, b := nuevoPaseo(70000), nuevoPaseo(70000)
	for i := 0; i < 500; i++ {
		o1, h1, l1, c1, v1 := a.vela(4)
		o2, h2, l2, c2, v2 := b.vela(4)
		if o1 != o2 || h1 != h2 || l1 != l2 || c1 != c2 || v1 != v2 {
			t.Fatalf("vela %d distinta entre dos paseos con la misma semilla", i)
		}
	}
}

// Y tiene que dar velas usables: OHLC coherente, volumen positivo y tanto
// velas alcistas como bajistas (hay comprobaciones que cuentan píxeles de
// cada color en el lienzo).
func TestPaseoDaVelasUsables(t *testing.T) {
	w := nuevoPaseo(70000)
	var suben, bajan int
	for i := 0; i < 3000; i++ {
		o, h, l, c, v := w.vela(6)
		if h < o || h < c || l > o || l > c {
			t.Fatalf("vela %d incoherente: o=%f h=%f l=%f c=%f", i, o, h, l, c)
		}
		if v <= 0 {
			t.Fatalf("vela %d con volumen %f", i, v)
		}
		if o <= 0 || l <= 0 {
			t.Fatalf("vela %d con precio no positivo", i)
		}
		if c > o {
			suben++
		} else if c < o {
			bajan++
		}
	}
	if suben < 500 || bajan < 500 {
		t.Fatalf("serie poco variada: %d velas al alza, %d a la baja", suben, bajan)
	}
}
