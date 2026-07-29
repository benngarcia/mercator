// Package tlsmaterialtest issues throwaway TLS material for tests.
//
// Nothing it produces is ever committed. Every certificate is generated inside
// the calling test's own temporary directory, is valid for one hour, and is
// discarded with that directory. A test that needs a real handshake needs a
// real certificate, and generating one is the only way to have that without a
// key in the repository.
package tlsmaterialtest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue writes a self-signed P-256 certificate for 127.0.0.1, ::1 and
// localhost into dir, and answers with the two file paths plus the pool a
// client verifies that certificate with.
func Issue(t *testing.T, dir string) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate throwaway key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mercator-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create throwaway certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse throwaway certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal throwaway key: %v", err)
	}
	certFile = writePEM(t, filepath.Join(dir, "mercator-test.crt"), "CERTIFICATE", der)
	keyFile = writePEM(t, filepath.Join(dir, "mercator-test.key"), "EC PRIVATE KEY", keyDER)
	pool = x509.NewCertPool()
	pool.AddCert(certificate)
	return certFile, keyFile, pool
}

func writePEM(t *testing.T, path, blockType string, der []byte) string {
	t.Helper()
	encoded := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
