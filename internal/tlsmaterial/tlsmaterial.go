// Package tlsmaterial loads the certificate and private key a Mercator process
// terminates TLS with.
//
// A deployment either names both files or names neither. Naming one is a
// configuration error, and a named file that cannot be loaded stops the
// process: the alternative, a listener that quietly serves plaintext because
// its certificate was missing, is exactly the failure this package exists to
// make impossible.
package tlsmaterial

import (
	"crypto/tls"
	"fmt"
)

const (
	// CertFileVar names the PEM certificate chain this process serves.
	CertFileVar = "MERCATOR_TLS_CERT_FILE"
	// KeyFileVar names the PEM private key belonging to that chain.
	KeyFileVar = "MERCATOR_TLS_KEY_FILE"
)

// Material names the two files. The zero Material is a deployment that
// configured neither, which is only legitimate on a loopback address; the
// process entrypoint decides that, because only it knows the listen address.
type Material struct {
	CertFile string
	KeyFile  string
}

// FromEnv reads both paths and refuses half a pair. It is refused here rather
// than at the listener so that a process which cannot serve what it was
// configured to serve never binds a port at all.
func FromEnv(getenv func(string) string) (Material, error) {
	material := Material{CertFile: getenv(CertFileVar), KeyFile: getenv(KeyFileVar)}
	switch {
	case material.CertFile == "" && material.KeyFile != "":
		return Material{}, fmt.Errorf("%s is set but %s is not", KeyFileVar, CertFileVar)
	case material.CertFile != "" && material.KeyFile == "":
		return Material{}, fmt.Errorf("%s is set but %s is not", CertFileVar, KeyFileVar)
	}
	return material, nil
}

// Configured reports whether this deployment named TLS material at all.
func (m Material) Configured() bool { return m.CertFile != "" }

// Config loads the pair into the server configuration Mercator serves it with:
// a TLS 1.2 floor, and HTTP/2 offered ahead of HTTP/1.1 through ALPN, because
// net/http serves HTTP/2 only over a connection whose handshake negotiated it.
// Every failure names the file that could not be used. Asking unconfigured
// material for a configuration is a caller mistake and says so rather than
// answering with a nil configuration somebody serves plaintext from.
func (m Material) Config() (*tls.Config, error) {
	if !m.Configured() {
		return nil, fmt.Errorf("no TLS material is configured; set %s and %s", CertFileVar, KeyFileVar)
	}
	certificate, err := tls.LoadX509KeyPair(m.CertFile, m.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load %s=%s with %s=%s: %w", CertFileVar, m.CertFile, KeyFileVar, m.KeyFile, err)
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}, nil
}
