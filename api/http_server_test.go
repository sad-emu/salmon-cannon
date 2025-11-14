package api

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"salmoncannon/config"
)

func TestHandleBridges_ReturnsJSONList(t *testing.T) {
	cfg := &config.SalmonCannonConfig{
		Bridges: []config.SalmonBridgeConfig{
			{Name: "bridge-one"},
			{Name: "bridge-two"},
		},
	}

	srv := NewServer(cfg, ":0")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bridges", nil)
	w := httptest.NewRecorder()

	srv.handleBridges(w, req)

	res := w.Result()
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200 got %d", res.StatusCode)
	}

	var list []bridgeDTO
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if got, want := len(list), 2; got != want {
		t.Fatalf("unexpected list length: got %d want %d", got, want)
	}

	if list[0].Name != "bridge-one" || list[0].ID != 0 {
		t.Fatalf("unexpected first element: %+v", list[0])
	}
	if list[1].Name != "bridge-two" || list[1].ID != 1 {
		t.Fatalf("unexpected second element: %+v", list[1])
	}
	if list[0].Circuit != "bridge-one" || list[0].ID != 0 {
		t.Fatalf("unexpected first element: %+v", list[0])
	}
	if list[1].Circuit != "bridge-two" || list[1].ID != 1 {
		t.Fatalf("unexpected second element: %+v", list[1])
	}
}

// generateTestCert generates a self-signed certificate and key for testing
func generateTestCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()

	// Generate private key
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate private key: %v", err)
	}

	// Create certificate template
	serialNumber, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("failed to generate serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"Test Organization"},
			CommonName:   "localhost",
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}

	// Create self-signed certificate
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	// Write certificate to temp file
	certFile = fmt.Sprintf("%s/test-cert-%d.pem", t.TempDir(), time.Now().UnixNano())
	certOut, err := os.Create(certFile)
	if err != nil {
		t.Fatalf("failed to create cert file: %v", err)
	}
	if err := pem.Encode(certOut, &pem.Block{Type: "CERTIFICATE", Bytes: certDER}); err != nil {
		t.Fatalf("failed to write cert: %v", err)
	}
	certOut.Close()

	// Write key to temp file
	keyFile = fmt.Sprintf("%s/test-key-%d.pem", t.TempDir(), time.Now().UnixNano())
	keyOut, err := os.Create(keyFile)
	if err != nil {
		t.Fatalf("failed to create key file: %v", err)
	}
	privBytes, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("failed to marshal private key: %v", err)
	}
	if err := pem.Encode(keyOut, &pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes}); err != nil {
		t.Fatalf("failed to write key: %v", err)
	}
	keyOut.Close()

	return certFile, keyFile
}

func TestServerTLS_WithValidCert(t *testing.T) {
	certFile, keyFile := generateTestCert(t)

	cfg := &config.SalmonCannonConfig{
		Bridges: []config.SalmonBridgeConfig{
			{Name: "test-bridge"},
		},
		ApiConfig: &config.ApiConfig{
			TLSCert: certFile,
			TLSKey:  keyFile,
		},
	}

	srv := NewServer(cfg, "127.0.0.1:0")
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Get the actual listening address
	addr := srv.ln.Addr().String()

	// Create HTTP client that accepts self-signed certs
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		},
	}

	// Test HTTPS request
	resp, err := client.Get(fmt.Sprintf("https://%s/api/v1/bridges", addr))
	if err != nil {
		t.Fatalf("failed to make HTTPS request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	var bridges []bridgeDTO
	if err := json.Unmarshal(body, &bridges); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(bridges) != 1 || bridges[0].Name != "test-bridge" {
		t.Fatalf("unexpected response: %+v", bridges)
	}
}

func TestServerHTTP_WithoutTLSConfig(t *testing.T) {
	cfg := &config.SalmonCannonConfig{
		Bridges: []config.SalmonBridgeConfig{
			{Name: "test-bridge"},
		},
		ApiConfig: nil, // No TLS config
	}

	srv := NewServer(cfg, "127.0.0.1:0")
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start server: %v", err)
	}
	defer srv.Stop()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Get the actual listening address
	addr := srv.ln.Addr().String()

	// Test HTTP request (not HTTPS)
	resp, err := http.Get(fmt.Sprintf("http://%s/api/v1/bridges", addr))
	if err != nil {
		t.Fatalf("failed to make HTTP request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read response body: %v", err)
	}

	var bridges []bridgeDTO
	if err := json.Unmarshal(body, &bridges); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(bridges) != 1 || bridges[0].Name != "test-bridge" {
		t.Fatalf("unexpected response: %+v", bridges)
	}
}
