// Package alerts evalúa alertas de precio contra las velas de 1s (F5,
// bloque 3).
//
// Vive en su PROPIO proceso (cmd/alerts). El colector no se toca: es la
// ingesta 24/7 con su ventana REST de 48 h, y un POST a Telegram que se
// cuelgue treinta segundos no puede acercarse a ella. Este proceso solo LEE
// velas y escribe en sus tablas; si se cae, la ingesta ni se entera.
//
// La máquina de estados es lo importante, así que va aparte y sin base de
// datos delante: Evaluar() es una función pura y sus tests no necesitan
// PostgreSQL.
//
// CONTRATO, que costó dos tests entender: cada segundo se evalúa UNA sola vez
// y solo cuando está CERRADO. La vela del segundo en curso no vale: el
// colector la reescribe cada 300 ms ensanchándola, y una vela cuyo rango
// abarca el nivel dispara en un sentido y luego en el otro cada vez que se la
// vuelve a pasar. De ahí Ventana(): nunca devuelve el segundo en curso, y la
// marca de agua avanza en la MISMA transacción que los disparos, así que
// reanudar tras una caída no repite ni se salta nada.
//
// Consecuencia asumida: si un segundo cruza el nivel al alza y vuelve a
// cruzarlo a la baja, se avisa del primero. Con velas de un segundo es un
// trato razonable.
package alerts

import "time"

// Lado del precio respecto al nivel, con banda muerta alrededor (Schmitt).
const (
	Debajo  = -1
	Dentro  = 0
	Encima  = 1
	SinLado = -99 // aún no sembrado: la primera vela coloca sin disparar
)

// Alerta es lo que el motor necesita para decidir. Los precios van en punto
// fijo 1e8, como las velas: comparar enteros, nunca floats.
type Alerta struct {
	ID          string
	Symbol      string
	Level       int64
	Direction   string // up | down | any
	Mode        string // once | recurring
	Status      string // armed | paused | done
	Note        string
	RearmBps    int
	CooldownSec int
	MaxPerDay   int
	Side        int // Debajo | Dentro | Encima | SinLado
	FiredCount  int // disparos de HOY (se reinicia al cambiar de día)
	LastFiredAt *time.Time
}

// Vela es lo mínimo de candles_1s que mira el motor.
type Vela struct {
	Ts               time.Time
	High, Low, Close int64
}

// Disparo es lo que hay que notificar y registrar.
type Disparo struct {
	Direccion string // up | down
	Precio    int64
	Vela      time.Time
	Entregar  bool   // false = se registra pero no se envía (cooldown, tope)
	Motivo    string // por qué no se entrega
	Pausar    bool   // la alerta se pausa sola tras el tope diario
	Terminar  bool   // 'once' cumplida
}

// banda: media anchura de la zona muerta alrededor del nivel. Suelo de 5 ticks
// (el tick de BTCUSDT perp es 0,1 USD) para que rearm_bps = 0 no degenere en
// una máquina de rebotes.
func (a Alerta) banda() int64 {
	b := a.Level * int64(a.RearmBps) / 10000
	const cincoTicks = 5 * 10_000_000 // 0,5 USD en escala 1e8
	if b < cincoTicks {
		b = cincoTicks
	}
	return b
}

// Evaluar aplica una vela a la alerta y devuelve el nuevo lado y, si toca, el
// disparo. NO toca la base de datos ni la red: es la parte que hay que poder
// probar sin levantar nada.
//
// El orden importa:
//  1. si estaba DEBAJO y el máximo alcanza el nivel  → cruce al alza
//  2. si estaba ENCIMA y el mínimo alcanza el nivel  → cruce a la baja
//  3. el cierre recoloca el lado, pero solo fuera de la banda
//
// Se mira [low, high] y no solo el cierre porque la vela de 1s dice que el
// precio VISITÓ ese rango: un pico que sube y vuelve dentro del mismo segundo
// es un cruce de verdad, y con el cierre se perdería.
func Evaluar(a Alerta, v Vela, ahora time.Time) (lado int, d *Disparo) {
	lado = a.Side
	eps := a.banda()

	if lado == SinLado {
		// Siembra: colocar sin disparar. Crear una alerta al precio actual no
		// puede notificar en el acto.
		return recolocar(lado, v.Close, a.Level, eps), nil
	}

	var dir string
	switch {
	case lado == Debajo && v.High >= a.Level:
		dir = "up"
	case lado == Encima && v.Low <= a.Level:
		dir = "down"
	}
	if dir == "" {
		return recolocar(lado, v.Close, a.Level, eps), nil
	}

	// Cruzado: el lado pasa a la banda muerta pase lo que pase. Aunque esta
	// alerta solo mire una dirección, el lado tiene que actualizarse o no
	// volvería a armarse nunca.
	lado = Dentro
	lado = recolocar(lado, v.Close, a.Level, eps)

	if a.Direction != "any" && a.Direction != dir {
		return lado, nil
	}

	dis := &Disparo{Direccion: dir, Precio: v.Close, Vela: v.Ts, Entregar: true}
	switch {
	case a.CooldownSec > 0 && a.LastFiredAt != nil &&
		ahora.Sub(*a.LastFiredAt) < time.Duration(a.CooldownSec)*time.Second:
		// El cruce SÍ se registra —el historial no miente— pero no se envía.
		dis.Entregar = false
		dis.Motivo = "cooldown"
	case a.MaxPerDay > 0 && a.FiredCount >= a.MaxPerDay:
		dis.Entregar = false
		dis.Motivo = "tope diario"
		dis.Pausar = true
	}
	if a.Mode == "once" {
		dis.Terminar = true
	}
	return lado, dis
}

func recolocar(lado int, cierre, nivel, eps int64) int {
	switch {
	case cierre >= nivel+eps:
		return Encima
	case cierre <= nivel-eps:
		return Debajo
	}
	return lado // dentro de la banda: se queda como estaba
}

// Ventana decide qué tramo hay que evaluar: lo que va DESPUÉS de la marca de
// agua y ANTES del último segundo conocido, que todavía se está formando.
//
// Los dos extremos son exclusivos a propósito:
//   - `marca` ya se evaluó y su transacción se confirmó; repetirla duplicaría.
//   - `ultimo` es la vela en curso, que aún puede ensancharse.
func Ventana(marca, ultimo time.Time) (desde, hasta time.Time, hay bool) {
	desde = marca.Add(time.Second)
	hasta = ultimo.Add(-time.Second)
	if hasta.Before(desde) {
		return marca, marca, false
	}
	return desde, hasta, true
}
