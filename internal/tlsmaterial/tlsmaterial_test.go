package tlsmaterial_test

import (
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	"github.com/benngarcia/mercator/internal/tlsmaterial"
	"github.com/benngarcia/mercator/internal/tlsmaterial/tlsmaterialtest"
)

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestAnEmptyEnvironmentConfiguresNoMaterial(t *testing.T) {
	// Arrange: a deployment that named neither file.
	getenv := env(nil)

	// Act
	material, err := tlsmaterial.FromEnv(getenv)

	// Assert
	if err != nil {
		t.Fatalf("read material: %v", err)
	}
	if material.Configured() {
		t.Fatalf("material = %+v, want unconfigured", material)
	}
}

func TestHalfAPairIsRefused(t *testing.T) {
	// Arrange: each half of the pair, on its own.
	cases := map[string]map[string]string{
		"certificate without key": {tlsmaterial.CertFileVar: "/etc/mercator/tls.crt"},
		"key without certificate": {tlsmaterial.KeyFileVar: "/etc/mercator/tls.key"},
	}

	for name, values := range cases {
		t.Run(name, func(t *testing.T) {
			// Act
			_, err := tlsmaterial.FromEnv(env(values))

			// Assert
			if err == nil {
				t.Fatal("half a pair should be refused")
			}
			if !strings.Contains(err.Error(), tlsmaterial.CertFileVar) ||
				!strings.Contains(err.Error(), tlsmaterial.KeyFileVar) {
				t.Fatalf("error = %q, want both variable names", err)
			}
		})
	}
}

func TestAnUnreadableCertificateNamesTheFile(t *testing.T) {
	// Arrange: a configured certificate path that is not there.
	missing := filepath.Join(t.TempDir(), "absent.crt")
	material, err := tlsmaterial.FromEnv(env(map[string]string{
		tlsmaterial.CertFileVar: missing,
		tlsmaterial.KeyFileVar:  filepath.Join(t.TempDir(), "absent.key"),
	}))
	if err != nil {
		t.Fatalf("read material: %v", err)
	}

	// Act
	config, err := material.Config()

	// Assert
	if config != nil {
		t.Fatal("an unloadable certificate must not yield a usable configuration")
	}
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v, want the certificate path %q", err, missing)
	}
}

func TestUnconfiguredMaterialRefusesToProduceAConfiguration(t *testing.T) {
	// Arrange
	var material tlsmaterial.Material

	// Act
	config, err := material.Config()

	// Assert
	if config != nil || err == nil {
		t.Fatalf("config = %v, err = %v; want a refusal", config, err)
	}
}

func TestALoadedPairFloorsAtTLS12AndOffersHTTP2(t *testing.T) {
	// Arrange: throwaway material on disk.
	certFile, keyFile, _ := tlsmaterialtest.Issue(t, t.TempDir())
	material, err := tlsmaterial.FromEnv(env(map[string]string{
		tlsmaterial.CertFileVar: certFile,
		tlsmaterial.KeyFileVar:  keyFile,
	}))
	if err != nil {
		t.Fatalf("read material: %v", err)
	}

	// Act
	config, err := material.Config()

	// Assert
	if err != nil {
		t.Fatalf("load material: %v", err)
	}
	if config.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %#x, want %#x", config.MinVersion, tls.VersionTLS12)
	}
	if got := strings.Join(config.NextProtos, ","); got != "h2,http/1.1" {
		t.Fatalf("NextProtos = %q, want \"h2,http/1.1\"", got)
	}
	if len(config.Certificates) != 1 {
		t.Fatalf("certificates = %d, want 1", len(config.Certificates))
	}
}
