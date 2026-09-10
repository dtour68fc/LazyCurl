package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/credentials/insecure"
)

func TestStripGRPCScheme(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"grpc scheme", "grpc://localhost:50051", "localhost:50051"},
		{"grpcs scheme", "grpcs://localhost:50051", "localhost:50051"},
		{"no scheme", "localhost:50051", "localhost:50051"},
		{"whitespace padded", "  grpc://localhost:50051  ", "localhost:50051"},
		{"empty", "", ""},
		{"host only, no scheme, no port", "my-service", "my-service"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := StripGRPCScheme(tt.in); got != tt.want {
				t.Errorf("StripGRPCScheme(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestInvokeGRPCValidatesConfigBeforeDialing(t *testing.T) {
	tests := []struct {
		name string
		cfg  *GRPCConfig
	}{
		{"nil config", nil},
		{"empty server", &GRPCConfig{Service: "x.Y", Method: "Z"}},
		{"empty service", &GRPCConfig{Server: "localhost:1", Method: "Z"}},
		{"empty method", &GRPCConfig{Server: "localhost:1", Service: "x.Y"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := InvokeGRPC(tt.cfg)
			if err == nil {
				t.Fatalf("InvokeGRPC(%+v) error = nil, want a validation error", tt.cfg)
			}
			if resp != nil {
				t.Errorf("InvokeGRPC(%+v) response = %+v, want nil on validation failure", tt.cfg, resp)
			}
		})
	}
}

// TestInvokeGRPCNothingListening exercises the real dial + reflection
// lookup path (dial via grpc.NewClient, strip the grpc:// scheme, attempt
// FileContainingSymbol) against a port nothing is listening on. This can't
// verify a successful invoke without a full reflection-enabled test
// server, but it does confirm the plumbing all the way through the
// reflection call doesn't panic and returns a clean wrapped error instead
// of hanging or crashing.
func TestInvokeGRPCNothingListening(t *testing.T) {
	// grpc-go's default connection backoff means a truly-nothing-listening
	// dial can otherwise take the full grpcInvokeTimeout to give up -
	// shrink it for this test so the suite doesn't eat 20+ seconds proving
	// a negative.
	old := grpcInvokeTimeout
	grpcInvokeTimeout = 500 * time.Millisecond
	defer func() { grpcInvokeTimeout = old }()

	resp, err := InvokeGRPC(&GRPCConfig{
		Server:  "grpc://127.0.0.1:1", // port 1 - nothing listens here
		Service: "test.EchoService",
		Method:  "Echo",
		Message: `{"message": "hi"}`,
	})
	if err == nil {
		t.Fatalf("InvokeGRPC() against nothing listening: error = nil, want a connection/reflection error")
	}
	if resp != nil {
		t.Errorf("InvokeGRPC() against nothing listening: response = %+v, want nil", resp)
	}
}

func TestGRPCTransportCredentialsDefaultsToInsecure(t *testing.T) {
	tests := []*GRPCTLSConfig{nil, {Enabled: false}}
	for _, tlsCfg := range tests {
		creds, err := grpcTransportCredentials(tlsCfg)
		if err != nil {
			t.Fatalf("grpcTransportCredentials(%+v) error = %v", tlsCfg, err)
		}
		if creds.Info().SecurityProtocol != insecure.NewCredentials().Info().SecurityProtocol {
			t.Errorf("grpcTransportCredentials(%+v) = %v, want insecure credentials", tlsCfg, creds.Info())
		}
	}
}

func TestGRPCTransportCredentialsRequiresBothCertAndKey(t *testing.T) {
	tests := []*GRPCTLSConfig{
		{Enabled: true, CertFile: "/tmp/only-cert.crt"},
		{Enabled: true, KeyFile: "/tmp/only-key.key"},
	}
	for _, tlsCfg := range tests {
		if _, err := grpcTransportCredentials(tlsCfg); err == nil {
			t.Errorf("grpcTransportCredentials(%+v) error = nil, want error (cert without key or vice versa)", tlsCfg)
		}
	}
}

func TestGRPCTransportCredentialsMissingCAFile(t *testing.T) {
	_, err := grpcTransportCredentials(&GRPCTLSConfig{
		Enabled: true,
		CAFile:  "/nonexistent/path/ca.crt",
	})
	if err == nil {
		t.Fatalf("grpcTransportCredentials() error = nil, want error for missing CA file")
	}
}

// TestGRPCTransportCredentialsFullMTLS builds a real throwaway self-signed
// cert+key+CA on disk and verifies grpcTransportCredentials successfully
// loads all of it into a working tls.Config (client cert present, CA pool
// populated) rather than just not-erroring.
func TestGRPCTransportCredentialsFullMTLS(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, caPath := writeTestCertFiles(t, dir)

	creds, err := grpcTransportCredentials(&GRPCTLSConfig{
		Enabled:    true,
		CertFile:   certPath,
		KeyFile:    keyPath,
		CAFile:     caPath,
		ServerName: "localhost",
	})
	if err != nil {
		t.Fatalf("grpcTransportCredentials() error = %v", err)
	}
	if creds.Info().ServerName != "localhost" {
		t.Errorf("ServerName = %q, want %q", creds.Info().ServerName, "localhost")
	}
}

// writeTestCertFiles generates a minimal self-signed cert/key pair (used
// as both the "CA" and the "client cert" for simplicity - the test only
// cares that grpcTransportCredentials can load real PEM files, not that
// they form a valid trust chain) and writes them to dir, returning their
// paths.
func writeTestCertFiles(t *testing.T, dir string) (certPath, keyPath, caPath string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate() error = %v", err)
	}

	certOut := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyOut := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})

	certPath = filepath.Join(dir, "client.crt")
	keyPath = filepath.Join(dir, "client.key")
	caPath = filepath.Join(dir, "ca.crt")

	if err := os.WriteFile(certPath, certOut, 0o600); err != nil {
		t.Fatalf("WriteFile(cert) error = %v", err)
	}
	if err := os.WriteFile(keyPath, keyOut, 0o600); err != nil {
		t.Fatalf("WriteFile(key) error = %v", err)
	}
	if err := os.WriteFile(caPath, certOut, 0o600); err != nil {
		t.Fatalf("WriteFile(ca) error = %v", err)
	}

	return certPath, keyPath, caPath
}
