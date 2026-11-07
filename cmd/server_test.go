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
