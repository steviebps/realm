package cmd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert writes a self-signed certificate with the given serial number and
// its key to dir, setting both files' modification time to mod.
func writeCert(t *testing.T, dir string, serial int64, mod time.Time) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: "realm.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	return certFile, keyFile
}

func serialOf(t *testing.T, c *tls.Certificate) int64 {
	t.Helper()
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf.SerialNumber.Int64()
}

func TestCertReloader(t *testing.T) {
	old := certCheckInterval
	certCheckInterval = 0
	t.Cleanup(func() { certCheckInterval = old })

	dir := t.TempDir()
	start := time.Now().Add(-time.Hour)
	certFile, keyFile := writeCert(t, dir, 1, start)
	c, err := newCertReloader(context.Background(), certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	get := func() int64 {
		t.Helper()
		cert, err := c.GetCertificate(nil)
		if err != nil {
			t.Fatal(err)
		}
		return serialOf(t, cert)
	}
	if got := get(); got != 1 {
		t.Fatalf("serial = %d, want 1", got)
	}

	// a renewal is picked up
	writeCert(t, dir, 2, start.Add(time.Minute))
	if got := get(); got != 2 {
		t.Fatalf("after renewal, serial = %d, want 2", got)
	}

	// a half-written renewal keeps the current certificate
	if err := os.WriteFile(certFile, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != 2 {
		t.Fatalf("with a broken file, serial = %d, want 2", got)
	}
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != 2 {
		t.Fatalf("with a missing key, serial = %d, want 2", got)
	}

	// a broken certificate stops startup
	if _, err := newCertReloader(context.Background(), certFile, keyFile); err == nil {
		t.Fatal("newCertReloader accepted a missing key")
	}
}

func TestRedactOptions(t *testing.T) {
	cases := []struct {
		name, option, value, want string
	}{
		{"etcd password", "password", "hunter2", "xxxxx"},
		{"etcd password reference", "password", "${REALM_ETCD_PASSWORD}", "xxxxx"},
		{"empty option", "password", "", ""},
		{"unknown option", "access_key", "hunter2", "xxxxx"},
		{"url password", "url", "postgres://realm:hunter2@db:5432/realm?sslmode=verify-full", "postgres://realm:xxxxx@db:5432/realm?sslmode=verify-full"},
		{"url password parameter", "url", "postgres://realm@db:5432/realm?sslmode=verify-full&password=hunter2", "xxxxx"},
		{"url sslpassword parameter", "url", "postgres://realm@db:5432/realm?sslpassword=hunter2", "xxxxx"},
		{"url password with an unescaped #", "url", "postgres://realm:hun#ter2@db:5432/realm", "xxxxx"},
		{"url password with an unescaped /", "url", "postgres://realm:1234/hunter2@db/realm", "xxxxx"},
		{"url password with a comma", "url", "postgres://realm:hun,ter2@db/realm", "postgres://realm:xxxxx@db/realm"},
		{"url user", "url", "postgres://realm@db:5432/realm", "postgres://realm@db:5432/realm"},
		{"keyword password", "url", "host=db user=realm password=hunter2", "xxxxx"},
		{"keyword password with spaces", "url", "host=db user=realm password = hunter2", "xxxxx"},
		{"keyword password in capitals", "url", "host=db user=realm PASSWORD=hunter2", "xxxxx"},
		{"url without credentials", "url", "postgres://db:5432/realm?sslmode=require", "postgres://db:5432/realm?sslmode=require"},
		{"keywords without credentials", "url", "host=db user=realm sslmode=require", "host=db user=realm sslmode=require"},
		{"endpoint password", "endpoint", "https://minio:hunter2@minio.internal:9000", "https://minio:xxxxx@minio.internal:9000"},
		{"endpoints", "endpoints", "https://etcd-0:2379,https://etcd-1:2379", "https://etcd-0:2379,https://etcd-1:2379"},
		{"username", "username", "realm", "realm"},
		{"key file", "key_file", "/etc/realm/etcd-client.key", "/etc/realm/etcd-client.key"},
		{"path", "path", "./.realm", "./.realm"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactOptions(map[string]string{c.option: c.value})
			if len(got) != 1 || got[c.option] != c.want {
				t.Fatalf("redactOptions(%s: %q) = %q, want %s: %q", c.option, c.value, got, c.option, c.want)
			}
		})
	}
}
