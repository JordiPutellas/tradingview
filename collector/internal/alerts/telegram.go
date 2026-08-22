package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Telegram manda los avisos. Sin token o sin chat, Configurado() es false y el
// motor sigue funcionando: los cruces se registran igual en alert_events y se
// ven en el panel. Es preferible a no evaluar nada por no poder avisar.
type Telegram struct {
	Token  string
	ChatID string
	Base   string // se sobreescribe en los tests
	HTTP   *http.Client
}

func TelegramDesdeEnv() *Telegram {
	return &Telegram{
		Token:  os.Getenv("TELEGRAM_BOT_TOKEN"),
		ChatID: os.Getenv("TELEGRAM_CHAT_ID"),
		Base:   env("TELEGRAM_API", "https://api.telegram.org"),
		HTTP:   &http.Client{Timeout: 15 * time.Second},
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (t *Telegram) Configurado() bool { return t != nil && t.Token != "" && t.ChatID != "" }

// Enviar manda un mensaje de texto. Devuelve el error de Telegram tal cual: el
// más común es "bot can't initiate conversation with a user", que significa
// que falta hablarle al bot una vez desde la aplicación.
func (t *Telegram) Enviar(ctx context.Context, texto string) error {
	if !t.Configurado() {
		return fmt.Errorf("telegram sin configurar")
	}
	destino := fmt.Sprintf("%s/bot%s/sendMessage", strings.TrimRight(t.Base, "/"), t.Token)
	datos := url.Values{"chat_id": {t.ChatID}, "text": {texto}, "disable_web_page_preview": {"true"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, destino, strings.NewReader(datos.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	cli := t.HTTP
	if cli == nil {
		cli = &http.Client{Timeout: 15 * time.Second}
	}
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var r struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("respuesta ilegible de telegram (HTTP %d)", resp.StatusCode)
	}
	if !r.OK {
		return fmt.Errorf("telegram: %s (HTTP %d)", r.Description, resp.StatusCode)
	}
	return nil
}

var madrid = func() *time.Location {
	l, err := time.LoadLocation("Europe/Madrid")
	if err != nil {
		return time.UTC
	}
	return l
}()

// Mensaje arma el texto del aviso: símbolo, nivel, precio actual y hora, que
// es lo que pidió el usuario. La hora va en Madrid, que es la que lee él, con
// la UTC detrás para que no haya dudas al comparar con el gráfico.
func Mensaje(symbol string, nivel, precio int64, direccion string, cuando time.Time, nota string) string {
	flecha := "▲ al alza"
	if direccion == "down" {
		flecha = "▼ a la baja"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "🔔 %s cruzó %s %s\n", symbol, dinero(nivel), flecha)
	fmt.Fprintf(&sb, "precio %s · %s (Madrid) · %s UTC",
		dinero(precio), cuando.In(madrid).Format("02/01/2006 15:04:05"), cuando.UTC().Format("15:04:05"))
	if nota != "" {
		fmt.Fprintf(&sb, "\nnota: %s", nota)
	}
	return sb.String()
}

// dinero pinta un precio en punto fijo 1e8 con dos decimales y separador de
// miles, como el resto de la interfaz.
func dinero(v int64) string {
	entero := v / 100_000_000
	dec := (v % 100_000_000) / 1_000_000
	if dec < 0 {
		dec = -dec
	}
	s := fmt.Sprintf("%d", entero)
	var partes []string
	for len(s) > 3 {
		partes = append([]string{s[len(s)-3:]}, partes...)
		s = s[:len(s)-3]
	}
	partes = append([]string{s}, partes...)
	return strings.Join(partes, ".") + fmt.Sprintf(",%02d", dec)
}
