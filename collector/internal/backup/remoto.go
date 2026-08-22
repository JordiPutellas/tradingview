package backup

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 es el destino FUERA del servidor. Un backup que solo vive en el mismo
// disco que la base de datos no protege del fallo que más probable es aquí:
// que el VPS desaparezca. Vale cualquier almacén compatible con S3; la
// recomendación es Cloudflare R2 (10 GB gratis y salida sin coste, que importa
// porque el simulacro de restauración descarga los ficheros).
//
// Sin credenciales configuradas el backup sigue funcionando: escribe en local
// y avisa. Es preferible a no copiar nada.
type S3 struct {
	Endpoint  string // p.ej. <cuenta>.r2.cloudflarestorage.com (sin https://)
	Bucket    string
	Prefijo   string
	AccessKey string
	SecretKey string
	Region    string
	SinTLS    bool
}

func S3DesdeEnv() (S3, bool) {
	c := S3{
		Endpoint:  os.Getenv("BACKUP_S3_ENDPOINT"),
		Bucket:    os.Getenv("BACKUP_S3_BUCKET"),
		Prefijo:   env("BACKUP_S3_PREFIX", "btcdash"),
		AccessKey: os.Getenv("BACKUP_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("BACKUP_S3_SECRET_KEY"),
		Region:    env("BACKUP_S3_REGION", "auto"),
		SinTLS:    os.Getenv("BACKUP_S3_INSECURE") == "1",
	}
	ok := c.Endpoint != "" && c.Bucket != "" && c.AccessKey != "" && c.SecretKey != ""
	return c, ok
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func (c S3) cliente() (*minio.Client, error) {
	return minio.New(c.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(c.AccessKey, c.SecretKey, ""),
		Secure:    !c.SinTLS,
		Region:    c.Region,
		Transport: transporte(),
	})
}

func transporte() http.RoundTripper {
	t, _ := minio.DefaultTransport(true)
	return t
}

// Subir manda los ficheros al bucket bajo Prefijo/clave. La clave es la misma
// ruta relativa que en local, así que el bucket es un espejo del directorio.
func (c S3) Subir(ctx context.Context, fs []Fichero) error {
	cli, err := c.cliente()
	if err != nil {
		return fmt.Errorf("cliente s3: %w", err)
	}
	for _, f := range fs {
		st, err := os.Stat(f.Ruta)
		if err != nil {
			return err
		}
		fh, err := os.Open(f.Ruta)
		if err != nil {
			return err
		}
		clave := path.Join(c.Prefijo, f.Clave)
		_, err = cli.PutObject(ctx, c.Bucket, clave, fh, st.Size(), minio.PutObjectOptions{
			ContentType:  tipo(f.Clave),
			UserMetadata: map[string]string{"sha256": f.SHA256},
		})
		fh.Close()
		if err != nil {
			return fmt.Errorf("subir %s: %w", clave, err)
		}
	}
	return nil
}

func tipo(clave string) string {
	switch {
	case strings.HasSuffix(clave, ".gz"):
		return "application/gzip"
	case strings.HasSuffix(clave, ".json"):
		return "application/json"
	}
	return "application/octet-stream"
}

// PodarRemoto borra objetos de una capa más viejos que keep días, dejando
// siempre al menos min. Misma política que en local: la rotación tiene que
// existir en los dos sitios o el bucket crece para siempre.
func (c S3) PodarRemoto(ctx context.Context, capa string, keep, min int) ([]string, error) {
	cli, err := c.cliente()
	if err != nil {
		return nil, err
	}
	prefijo := path.Join(c.Prefijo, capa) + "/"
	type obj struct {
		clave string
		mod   time.Time
	}
	var objs []obj
	for o := range cli.ListObjects(ctx, c.Bucket, minio.ListObjectsOptions{Prefix: prefijo, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		objs = append(objs, obj{o.Key, o.LastModified})
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].mod.After(objs[j].mod) })
	limite := time.Now().Add(-time.Duration(keep) * 24 * time.Hour)
	var borrados []string
	for i, o := range objs {
		if i < min || o.mod.After(limite) {
			continue
		}
		if err := cli.RemoveObject(ctx, c.Bucket, o.clave, minio.RemoveObjectOptions{}); err != nil {
			return borrados, err
		}
		borrados = append(borrados, o.clave)
	}
	return borrados, nil
}

// Listar devuelve las claves de una capa, de la más nueva a la más vieja.
// Sirve para comprobar desde fuera que el backup llegó.
func (c S3) Listar(ctx context.Context, capa string) ([]string, error) {
	cli, err := c.cliente()
	if err != nil {
		return nil, err
	}
	var claves []string
	prefijo := path.Join(c.Prefijo, capa)
	for o := range cli.ListObjects(ctx, c.Bucket, minio.ListObjectsOptions{Prefix: prefijo, Recursive: true}) {
		if o.Err != nil {
			return nil, o.Err
		}
		claves = append(claves, o.Key)
	}
	return claves, nil
}
