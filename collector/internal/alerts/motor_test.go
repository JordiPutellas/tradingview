package alerts

import (
	"testing"
	"time"
)

const e8 = 100_000_000

func precio(x float64) int64 { return int64(x * e8) }

func vela(h, l, c float64) Vela {
	return Vela{Ts: time.Unix(1_787_000_000, 0).UTC(), High: precio(h), Low: precio(l), Close: precio(c)}
}

func base() Alerta {
	return Alerta{
		ID: "a", Symbol: "BTCUSDT", Level: precio(70000), Direction: "any", Mode: "recurring",
		Status: "armed", RearmBps: 5, CooldownSec: 0, MaxPerDay: 20, Side: SinLado,
	}
}

var t0 = time.Unix(1_787_000_000, 0).UTC()

// La primera vela coloca el lado y NO dispara: crear una alerta al precio
// actual no puede notificar en el acto.
func TestLaPrimeraVelaSiembraSinDisparar(t *testing.T) {
	for _, c := range []struct {
		nombre string
		v      Vela
		lado   int
	}{
		{"por encima", vela(70100, 70050, 70080), Encima},
		{"por debajo", vela(69950, 69900, 69920), Debajo},
	} {
		lado, d := Evaluar(base(), c.v, t0)
		if d != nil {
			t.Errorf("%s: ha disparado en la siembra", c.nombre)
		}
		if lado != c.lado {
			t.Errorf("%s: lado %d, esperaba %d", c.nombre, lado, c.lado)
		}
	}
}

// Un pico DENTRO del segundo cuenta: la vela dice que el precio visitó el
// rango, aunque cierre otra vez por debajo. Mirando solo el cierre se
// perdería el cruce.
func TestCruceDentroDeLaVela(t *testing.T) {
	a := base()
	a.Side = Debajo
	lado, d := Evaluar(a, vela(70010, 69900, 69950), t0)
	if d == nil {
		t.Fatal("un máximo por encima del nivel es un cruce al alza")
	}
	if d.Direccion != "up" {
		t.Fatalf("dirección %q", d.Direccion)
	}
	// Cierra por debajo de la banda: vuelve a quedar armada hacia arriba.
	if lado != Debajo {
		t.Fatalf("lado tras cerrar abajo: %d", lado)
	}
}

// El caso que pidió el usuario: precio oscilando en el nivel no puede mandar
// veinte mensajes. Para rearmar hay que SALIR de la banda.
func TestElReboteEnElNivelNoDisparaDosVeces(t *testing.T) {
	a := base()
	a.Side = Debajo
	lado, d := Evaluar(a, vela(70020, 69990, 70005), t0)
	if d == nil {
		t.Fatal("el primer cruce debería disparar")
	}
	a.Side = lado
	// Dentro de la banda (5 bps de 70.000 son 35 USD): no rearma.
	for i, v := range []Vela{
		vela(70010, 69995, 70002),
		vela(70015, 69998, 70008),
		vela(70020, 69999, 70001),
	} {
		lado, d = Evaluar(a, v, t0)
		if d != nil {
			t.Fatalf("rebote %d: ha vuelto a disparar sin salir de la banda", i)
		}
		a.Side = lado
	}
	// Sale por abajo de verdad: rearma.
	lado, d = Evaluar(a, vela(69980, 69940, 69950), t0)
	if d != nil {
		t.Fatal("salir de la banda no es un cruce")
	}
	if lado != Debajo {
		t.Fatalf("tras salir por abajo el lado es %d", lado)
	}
	a.Side = lado
	if _, d = Evaluar(a, vela(70050, 70000, 70040), t0); d == nil {
		t.Fatal("tras rearmar, el siguiente cruce sí dispara")
	}
}

// El filtro de dirección se aplica AL EMITIR, no a la máquina de estados: una
// alerta 'up' tiene que seguir el precio hacia abajo o no se rearma nunca.
func TestDireccionFiltraPeroNoCongelaElLado(t *testing.T) {
	a := base()
	a.Direction = "up"
	a.Side = Encima
	lado, d := Evaluar(a, vela(70000, 69900, 69920), t0)
	if d != nil {
		t.Fatal("una alerta 'up' no avisa de un cruce a la baja")
	}
	if lado != Debajo {
		t.Fatalf("pero el lado sí baja: %d", lado)
	}
	a.Side = lado
	if _, d := Evaluar(a, vela(70100, 70000, 70080), t0); d == nil {
		t.Fatal("y luego sí avisa del cruce al alza")
	}
}

func TestCooldownRegistraPeroNoEnvia(t *testing.T) {
	a := base()
	a.Side = Debajo
	a.CooldownSec = 300
	hace1m := t0.Add(-time.Minute)
	a.LastFiredAt = &hace1m
	_, d := Evaluar(a, vela(70100, 70000, 70080), t0)
	if d == nil {
		t.Fatal("el cruce existe y tiene que quedar registrado")
	}
	if d.Entregar {
		t.Fatal("dentro del cooldown no se envía")
	}
	if d.Motivo != "cooldown" {
		t.Fatalf("motivo %q", d.Motivo)
	}
	// Pasado el cooldown, sí.
	hace10m := t0.Add(-10 * time.Minute)
	a.LastFiredAt = &hace10m
	if _, d = Evaluar(a, vela(70100, 70000, 70080), t0); !d.Entregar {
		t.Fatal("pasado el cooldown debería entregarse")
	}
}

func TestTopeDiarioPausaLaAlerta(t *testing.T) {
	a := base()
	a.Side = Debajo
	a.MaxPerDay = 3
	a.FiredCount = 3
	_, d := Evaluar(a, vela(70100, 70000, 70080), t0)
	if d == nil || d.Entregar {
		t.Fatal("pasado el tope se registra pero no se envía")
	}
	if !d.Pausar {
		t.Fatal("y la alerta se pausa sola")
	}
}

func TestUnaVezSeMarcaTerminada(t *testing.T) {
	a := base()
	a.Mode = "once"
	a.Side = Debajo
	_, d := Evaluar(a, vela(70100, 70000, 70080), t0)
	if d == nil || !d.Terminar {
		t.Fatal("una alerta de 'solo una vez' termina al disparar")
	}
}

// Una vela cuyo rango abarca el nivel cruza en los dos sentidos: se avisa del
// primero y nada más. Sin esto —y es lo que pasaba— cada repaso de la misma
// vela alternaba 'up' y 'down' sin fin.
func TestUnaVelaQueAbarcaElNivelAvisaUnaSolaVez(t *testing.T) {
	a := base()
	a.Side = Debajo
	lado, d := Evaluar(a, vela(70100, 69900, 70080), t0)
	if d == nil || d.Direccion != "up" {
		t.Fatal("el primer cruce es al alza")
	}
	if lado != Encima {
		t.Fatalf("y el lado queda arriba: %d", lado)
	}
}

// La ventana de evaluación nunca puede incluir el segundo en curso, ni repetir
// lo ya visto.
func TestVentanaNoIncluyeElSegundoEnCurso(t *testing.T) {
	marca := time.Unix(1_787_000_000, 0).UTC()
	ultimo := marca.Add(5 * time.Second)

	desde, hasta, hay := Ventana(marca, ultimo)
	if !hay {
		t.Fatal("con cinco segundos nuevos hay trabajo")
	}
	if !desde.After(marca) {
		t.Fatalf("desde=%v debería ser posterior a la marca (exclusivo)", desde)
	}
	if !hasta.Before(ultimo) {
		t.Fatalf("hasta=%v no puede llegar al segundo en curso %v", hasta, ultimo)
	}

	// Sin velas nuevas cerradas, no hay ventana.
	if _, _, hay := Ventana(ultimo.Add(-time.Second), ultimo); hay {
		t.Fatal("solo está el segundo en curso: no hay nada que evaluar")
	}
	if _, _, hay := Ventana(ultimo, ultimo); hay {
		t.Fatal("marca al día: no hay nada que evaluar")
	}
}
