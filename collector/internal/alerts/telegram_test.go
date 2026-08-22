package alerts

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// El bot de Telegram del usuario aún no existe, así que el camino de envío se
// prueba contra un servidor falso: qué URL se llama, qué se manda y qué se
// hace con un error de la API (el clásico "bot can't initiate conversation").
func TestEnviarPorTelegram(t *testing.T) {
	var ruta, cuerpo string
	respuesta := `{"ok":true,"result":{"message_id":1}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ruta = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		cuerpo = string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, respuesta)
	}))
	defer srv.Close()

	tg := &Telegram{Token: "123:ABC", ChatID: "-1001", Base: srv.URL, HTTP: srv.Client()}
	if !tg.Configurado() {
		t.Fatal("con token y chat debería estar configurado")
	}
	if err := tg.Enviar(context.Background(), "hola"); err != nil {
		t.Fatalf("enviar: %v", err)
	}
	if ruta != "/bot123:ABC/sendMessage" {
		t.Fatalf("ruta %q", ruta)
	}
	vals, _ := url.ParseQuery(cuerpo)
	if vals.Get("chat_id") != "-1001" || vals.Get("text") != "hola" {
		t.Fatalf("cuerpo %q", cuerpo)
	}

	// Error de la API: tiene que llegar tal cual, no tragarse.
	respuesta = `{"ok":false,"description":"Forbidden: bot can't initiate conversation with a user"}`
	err := tg.Enviar(context.Background(), "hola")
	if err == nil || !strings.Contains(err.Error(), "initiate conversation") {
		t.Fatalf("el error de telegram debería propagarse: %v", err)
	}
}

func TestSinConfigurarNoEnvia(t *testing.T) {
	tg := &Telegram{Base: "http://no-existe"}
	if tg.Configurado() {
		t.Fatal("sin token no está configurado")
	}
	if err := tg.Enviar(context.Background(), "x"); err == nil {
		t.Fatal("debería negarse a enviar")
	}
}

func TestMensajeLlevaLoQuePidioElUsuario(t *testing.T) {
	cuando := time.Date(2026, 8, 22, 14, 41, 7, 0, time.UTC)
	m := Mensaje("BTCUSDT", 7_000_000_000_000, 7_008_430_000_000, "up", cuando, "soporte semanal")
	for _, quiero := range []string{
		"BTCUSDT",      // símbolo
		"70.000,00",    // nivel
		"70.084,30",    // precio actual
		"al alza",      // dirección
		"16:41:07",     // hora de Madrid (UTC+2 en agosto)
		"14:41:07 UTC", // y la UTC detrás
		"soporte semanal",
	} {
		if !strings.Contains(m, quiero) {
			t.Errorf("el mensaje no lleva %q:\n%s", quiero, m)
		}
	}
}

func TestDinero(t *testing.T) {
	casos := map[int64]string{
		7_000_000_000_000:   "70.000,00",
		123_450_000_000:     "1.234,50",
		9_990_000_000:       "99,90",
		100_000_000_000_000: "1.000.000,00",
	}
	for v, quiero := range casos {
		if got := dinero(v); got != quiero {
			t.Errorf("dinero(%d) = %q, esperaba %q", v, got, quiero)
		}
	}
}
