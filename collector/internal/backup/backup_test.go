package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// falsoS3 es lo justo del protocolo de S3 para que minio-go crea que habla con
// un bucket: responder al sondeo de región, aceptar PUT, listar y borrar.
//
// Existe porque el destino real (Cloudflare R2) necesita credenciales del
// usuario y no las hay: sin esto, el camino de subida no se probaría nunca y
// el primer backup de verdad sería también la primera ejecución del código.
type falsoS3 struct {
	mu       sync.Mutex
	objetos  map[string][]byte
	borrados []string
	srv      *httptest.Server
}

func nuevoFalsoS3() *falsoS3 {
	f := &falsoS3{objetos: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.servir))
	return f
}

func (f *falsoS3) servir(w http.ResponseWriter, r *http.Request) {
	clave := strings.TrimPrefix(r.URL.Path, "/")
	if i := strings.Index(clave, "/"); i >= 0 {
		clave = clave[i+1:] // fuera el nombre del bucket
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Query().Has("location"):
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<?xml version="1.0" encoding="UTF-8"?><LocationConstraint>auto</LocationConstraint>`)
	case r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2":
		f.listar(w, r)
	case r.Method == http.MethodPut:
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// minio-go firma el cuerpo por trozos (aws-chunked) cuando el destino
		// es HTTP: lo que llega no es el fichero, sino trozos con cabecera. S3
		// de verdad lo deshace; aquí también, o el test compararía basura.
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") ||
			r.Header.Get("X-Amz-Content-Sha256") == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
			b = desTrocear(b)
		}
		f.mu.Lock()
		f.objetos[clave] = b
		f.mu.Unlock()
		w.Header().Set("ETag", `"d41d8cd98f00b204e9800998ecf8427e"`)
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.objetos, clave)
		f.borrados = append(f.borrados, clave)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodHead:
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusOK)
	}
}

func (f *falsoS3) listar(w http.ResponseWriter, r *http.Request) {
	prefijo := r.URL.Query().Get("prefix")
	f.mu.Lock()
	defer f.mu.Unlock()
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
	for k := range f.objetos {
		if !strings.HasPrefix(k, prefijo) {
			continue
		}
		// Fecha vieja a propósito: así la poda tiene algo que borrar.
		sb.WriteString(fmt.Sprintf("<Contents><Key>%s</Key><LastModified>%s</LastModified><Size>%d</Size></Contents>",
			k, time.Now().AddDate(0, 0, -400).UTC().Format(time.RFC3339), len(f.objetos[k])))
	}
	sb.WriteString(`</ListBucketResult>`)
	w.Header().Set("Content-Type", "application/xml")
	fmt.Fprint(w, sb.String())
}

// desTrocear deshace el formato aws-chunked: "<tamaño hex>;chunk-signature=…\r\n<datos>\r\n"
// repetido hasta un trozo de tamaño 0.
func desTrocear(b []byte) []byte {
	var out []byte
	for len(b) > 0 {
		i := bytes.Index(b, []byte("\r\n"))
		if i < 0 {
			break
		}
		cab := string(b[:i])
		if j := strings.Index(cab, ";"); j >= 0 {
			cab = cab[:j]
		}
		n, err := strconv.ParseInt(strings.TrimSpace(cab), 16, 64)
		if err != nil || n == 0 {
			break
		}
		b = b[i+2:]
		if int64(len(b)) < n {
			break
		}
		out = append(out, b[:n]...)
		b = b[n:]
		if len(b) >= 2 {
			b = b[2:]
		}
	}
	return out
}

func (f *falsoS3) config() S3 {
	u, _ := url.Parse(f.srv.URL)
	return S3{Endpoint: u.Host, Bucket: "backups", Prefijo: "btcdash",
		AccessKey: "clave", SecretKey: "secreto", Region: "auto", SinTLS: true}
}

func TestSubirYPodarRemoto(t *testing.T) {
	fs3 := nuevoFalsoS3()
	defer fs3.srv.Close()

	dir := t.TempDir()
	ruta := filepath.Join(dir, "estado", "2026-08-22T1200Z")
	if err := os.MkdirAll(ruta, 0o755); err != nil {
		t.Fatal(err)
	}
	contenido := []byte("ts,symbol\n1,BTCUSDT\n")
	if err := os.WriteFile(filepath.Join(ruta, "drawings.csv.gz"), contenido, 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := describir(filepath.Join(ruta, "drawings.csv.gz"), dir)
	if err != nil {
		t.Fatal(err)
	}
	if f.Clave != "estado/2026-08-22T1200Z/drawings.csv.gz" {
		t.Fatalf("clave inesperada: %s", f.Clave)
	}

	c := fs3.config()
	if err := c.Subir(context.Background(), []Fichero{f}); err != nil {
		t.Fatalf("subir: %v", err)
	}
	fs3.mu.Lock()
	subido, ok := fs3.objetos["btcdash/"+f.Clave]
	fs3.mu.Unlock()
	if !ok {
		t.Fatalf("el objeto no llegó; hay %v", claves(fs3))
	}
	if string(subido) != string(contenido) {
		t.Fatalf("contenido distinto: %q", string(subido))
	}

	// La poda remota tiene que borrar lo viejo... salvo el mínimo que se guarda
	// siempre. Con un solo objeto y min=3, no puede borrar nada.
	borrados, err := c.PodarRemoto(context.Background(), "estado", 30, 3)
	if err != nil {
		t.Fatalf("podar: %v", err)
	}
	if len(borrados) != 0 {
		t.Fatalf("ha borrado la única copia que había: %v", borrados)
	}
	// Con min=0 sí: el objeto es de hace 400 días.
	borrados, err = c.PodarRemoto(context.Background(), "estado", 30, 0)
	if err != nil {
		t.Fatalf("podar: %v", err)
	}
	if len(borrados) != 1 {
		t.Fatalf("esperaba borrar 1 objeto viejo, borró %v", borrados)
	}
}

func claves(f *falsoS3) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.objetos {
		out = append(out, k)
	}
	return out
}

func TestS3DesdeEnvExigeLasCuatro(t *testing.T) {
	for _, falta := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_ACCESS_KEY", "BACKUP_S3_SECRET_KEY"} {
		for _, k := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_ACCESS_KEY", "BACKUP_S3_SECRET_KEY"} {
			t.Setenv(k, "x")
		}
		t.Setenv(falta, "")
		if _, ok := S3DesdeEnv(); ok {
			t.Fatalf("sin %s no debería darse por configurado", falta)
		}
	}
	for _, k := range []string{"BACKUP_S3_ENDPOINT", "BACKUP_S3_BUCKET", "BACKUP_S3_ACCESS_KEY", "BACKUP_S3_SECRET_KEY"} {
		t.Setenv(k, "x")
	}
	if _, ok := S3DesdeEnv(); !ok {
		t.Fatal("con las cuatro variables debería estar configurado")
	}
}

// La poda local nunca puede dejar el directorio vacío: si algo va mal con las
// fechas, es preferible gastar disco a quedarse sin copias.
func TestPodarLocalRespetaElMinimo(t *testing.T) {
	dir := t.TempDir()
	capa := filepath.Join(dir, "estado")
	os.MkdirAll(capa, 0o755)
	viejo := time.Now().AddDate(0, 0, -100)
	for i := 0; i < 5; i++ {
		ruta := filepath.Join(capa, fmt.Sprintf("copia-%d", i))
		os.WriteFile(ruta, []byte("x"), 0o644)
		os.Chtimes(ruta, viejo, viejo.Add(time.Duration(i)*time.Hour))
	}
	borrados, err := Podar(dir, "estado", 30, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(borrados) != 2 {
		t.Fatalf("esperaba borrar 2 y dejar 3, borró %d", len(borrados))
	}
	quedan, _ := os.ReadDir(capa)
	if len(quedan) != 3 {
		t.Fatalf("quedan %d copias, esperaba 3", len(quedan))
	}
}
