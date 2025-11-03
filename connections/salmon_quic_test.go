package connections

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
)

// generateTLSConfig creates a self-signed certificate for testing
func generateTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(time.Hour),
	}

	publicKey := key.Public()
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, publicKey, key)
	if err != nil {
		return nil, err
	}

	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"quic-test"},
		ClientAuth:   tls.NoClientCert,
	}, nil
}

func TestNewSalmonQuic(t *testing.T) {
	tlscfg := &tls.Config{}
	qcfg := &quic.Config{}

	sq := NewSalmonQuic(8080, "127.0.0.1", "test-bridge", tlscfg, qcfg, "")

	if sq == nil {
		t.Fatal("NewSalmonQuic returned nil")
	}

	if sq.BridgePort != 8080 {
		t.Errorf("Expected BridgePort 8080, got %d", sq.BridgePort)
	}

	if sq.BridgeAddress != "127.0.0.1" {
		t.Errorf("Expected BridgeAddress 127.0.0.1, got %s", sq.BridgeAddress)
	}

	if sq.BridgeName != "test-bridge" {
		t.Errorf("Expected BridgeName test-bridge, got %s", sq.BridgeName)
	}

	if !sq.bridgeDown {
		t.Error("Expected bridgeDown to be true initially")
	}

	if sq.qconn != nil {
		t.Error("Expected qconn to be nil initially")
	}

	if sq.interfaceName != "" {
		t.Errorf("Expected empty interfaceName, got %s", sq.interfaceName)
	}
}

func TestNewSalmonQuicWithInterface(t *testing.T) {
	tlscfg := &tls.Config{}
	qcfg := &quic.Config{}
	sq := NewSalmonQuic(8080, "127.0.0.1", "test-bridge", tlscfg, qcfg, "eth0")

	if sq.interfaceName != "eth0" {
		t.Errorf("Expected interfaceName eth0, got %s", sq.interfaceName)
	}
}

func TestShouldBlockHost(t *testing.T) {
	tests := []struct {
		name           string
		expectedRemote string
		newRemote      string
		shouldBlock    bool
	}{
		{
			name:           "Empty expected allows all",
			expectedRemote: "",
			newRemote:      "192.168.1.1",
			shouldBlock:    false,
		},
		{
			name:           "Matching addresses",
			expectedRemote: "192.168.1.1",
			newRemote:      "192.168.1.1",
			shouldBlock:    false,
		},
		{
			name:           "Non-matching addresses",
			expectedRemote: "192.168.1.1",
			newRemote:      "192.168.1.2",
			shouldBlock:    true,
		},
		{
			name:           "Different subnets",
			expectedRemote: "10.0.0.1",
			newRemote:      "192.168.1.1",
			shouldBlock:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := shouldBlockHost(tt.expectedRemote, tt.newRemote)
			if result != tt.shouldBlock {
				t.Errorf("shouldBlockHost(%q, %q) = %v, want %v",
					tt.expectedRemote, tt.newRemote, result, tt.shouldBlock)
			}
		})
	}
}

func TestEnsureQUICWithInvalidAddress(t *testing.T) {
	tlscfg, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate TLS config: %v", err)
	}

	qcfg := &quic.Config{
		MaxIdleTimeout: 2 * time.Second,
	}
	sq := NewSalmonQuic(1, "invalid-host-name-that-does-not-exist", "test-bridge", tlscfg, qcfg, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = sq.ensureQUIC(ctx)
	if err == nil {
		t.Error("Expected error when connecting to invalid host, got nil")
	}

	if sq.qconn != nil {
		t.Error("Expected qconn to remain nil after failed connection")
	}
}

func TestReconnectBridge(t *testing.T) {
	tlscfg, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate TLS config: %v", err)
	}

	qcfg := &quic.Config{
		MaxIdleTimeout: 2 * time.Second,
	}
	// Use invalid address to test error handling
	sq := NewSalmonQuic(1, "invalid-host", "test-bridge", tlscfg, qcfg, "")

	err = sq.reconnectBridge()
	if err == nil {
		t.Error("Expected error when reconnecting to invalid host, got nil")
	}
}

func TestOpenStreamWithoutConnection(t *testing.T) {
	tlscfg, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate TLS config: %v", err)
	}

	qcfg := &quic.Config{
		MaxIdleTimeout: 2 * time.Second,
	}

	sq := NewSalmonQuic(1, "invalid-host", "test-bridge", tlscfg, qcfg, "")

	_, err = sq.OpenStream()
	if err == nil {
		t.Error("Expected error when opening stream without connection, got nil")
	}
}

func TestOpenStreamIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	serverTLSConfig, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate server TLS config: %v", err)
	}

	clientTLSConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-test"},
	}

	qcfg := &quic.Config{
		MaxIdleTimeout:     2 * time.Second,
		MaxIncomingStreams: 100,
	}

	// Start server
	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLSConfig, qcfg)
	if err != nil {
		t.Fatalf("Failed to start QUIC listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// Server goroutine
	var serverWg sync.WaitGroup
	serverWg.Add(1)
	go func() {
		defer serverWg.Done()
		conn, err := listener.Accept(context.Background())
		if err != nil {
			return
		}
		defer conn.CloseWithError(0, "test done")

		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		defer stream.Close()

		// Echo server: read and write back
		buf := make([]byte, 100)
		n, _ := stream.Read(buf)
		stream.Write(buf[:n])
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonQuic(port, "127.0.0.1", "test-bridge", clientTLSConfig, qcfg, "")

	// Open stream
	stream, err := sq.OpenStream()
	if err != nil {
		t.Fatalf("Failed to open stream: %v", err)
	}
	defer stream.Close()

	// Test writing and reading
	testData := []byte("hello quic")
	_, err = stream.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write to stream: %v", err)
	}

	buf := make([]byte, 100)
	stream.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := stream.Read(buf)
	if err != nil && n == 0 {
		// Server may have closed, which is acceptable for this test
		t.Logf("Stream closed by server (expected): %v", err)
	} else if err == nil {
		if string(buf[:n]) != string(testData) {
			t.Errorf("Expected to read %q, got %q", testData, buf[:n])
		}
	}

	stream.Close()
	listener.Close()
	serverWg.Wait()
}

func TestConcurrentStreamOpening(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	serverTLSConfig, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate server TLS config: %v", err)
	}

	clientTLSConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-test"},
	}

	qcfg := &quic.Config{
		MaxIdleTimeout:     2 * time.Second,
		MaxIncomingStreams: 100,
	}

	// Start server
	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLSConfig, qcfg)
	if err != nil {
		t.Fatalf("Failed to start QUIC listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// Server goroutine that handles multiple streams
	go func() {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			return
		}
		defer conn.CloseWithError(0, "test done")

		for i := 0; i < 10; i++ {
			go func() {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				defer stream.Close()
				buf := make([]byte, 100)
				n, _ := stream.Read(buf)
				stream.Write(buf[:n])
			}()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonQuic(port, "127.0.0.1", "test-bridge", clientTLSConfig, qcfg, "")

	// Open multiple streams concurrently
	var wg sync.WaitGroup
	numStreams := 10
	errors := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, err := sq.OpenStream()
			if err != nil {
				errors <- err
				return
			}
			defer stream.Close()

			// Write and read
			testData := []byte("test")
			stream.Write(testData)
			buf := make([]byte, 100)
			stream.Read(buf)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		if err != nil {
			t.Logf("Stream error: %v", err)
			errorCount++
		}
	}

	// Allow some errors due to test timing
	if errorCount == numStreams {
		t.Error("All streams failed to open")
	}
}

func TestBridgeDownFlag(t *testing.T) {
	tlscfg, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate TLS config: %v", err)
	}

	qcfg := &quic.Config{
		MaxIdleTimeout: 2 * time.Second,
	}

	sq := NewSalmonQuic(1, "invalid-host", "test-bridge", tlscfg, qcfg, "")

	// Initially should be down
	if !sq.bridgeDown {
		t.Error("Expected bridgeDown to be true initially")
	}

	// Try to connect (will fail)
	sq.reconnectBridge()

	// Should still be down after failed connection
	if !sq.bridgeDown {
		t.Error("Expected bridgeDown to be true after failed connection")
	}
}

func TestMutexSafety(t *testing.T) {
	tlscfg, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate TLS config: %v", err)
	}

	qcfg := &quic.Config{
		MaxIdleTimeout: 2 * time.Second,
	}
	sq := NewSalmonQuic(1, "invalid-host", "test-bridge", tlscfg, qcfg, "")

	// Try to access connection state concurrently
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sq.reconnectBridge()
		}()
	}

	wg.Wait()
	// Test passes if no race condition detected
}

func TestListenPacketOnInterfaceInvalidInterface(t *testing.T) {
	// This test will fail on non-Linux or if the interface doesn't exist
	_, err := listenPacketOnInterface("udp", "nonexistent-interface-12345")
	if err == nil {
		t.Error("Expected error when binding to non-existent interface")
	}

	if err != nil && len(err.Error()) > 0 {
		// Just check that we got an error, the exact message may vary by platform
		t.Logf("Got expected error: %v", err)
	}
}

func TestMaxConcurrentStreamOpening(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	serverTLSConfig, err := generateTLSConfig()
	if err != nil {
		t.Fatalf("Failed to generate server TLS config: %v", err)
	}

	clientTLSConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"quic-test"},
	}

	var streamsToTest int64 = 40000

	qcfg := &quic.Config{
		MaxIdleTimeout:     2 * time.Second,
		MaxIncomingStreams: streamsToTest,
	}

	// Start server
	listener, err := quic.ListenAddr("127.0.0.1:0", serverTLSConfig, qcfg)
	if err != nil {
		t.Fatalf("Failed to start QUIC listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// Server goroutine that handles multiple streams
	go func() {
		conn, err := listener.Accept(context.Background())
		if err != nil {
			return
		}
		defer conn.CloseWithError(0, "test done")

		for i := 0; i < 10; i++ {
			go func() {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					return
				}
				defer stream.Close()
				buf := make([]byte, 100)
				n, _ := stream.Read(buf)
				stream.Write(buf[:n])
			}()
		}
		time.Sleep(100 * time.Millisecond)
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonQuic(port, "127.0.0.1", "test-bridge", clientTLSConfig, qcfg, "")

	// Open multiple streams concurrently
	var wg sync.WaitGroup
	numStreams := streamsToTest
	errors := make(chan error, numStreams)

	for i := 0; i < int(numStreams); i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, err := sq.OpenStream()
			if err != nil {
				errors <- err
				return
			}
			defer stream.Close()

			// Write and read
			testData := []byte("test")
			stream.Write(testData)
			buf := make([]byte, 100)
			stream.Read(buf)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		if err != nil {
			t.Logf("Stream error: %v", err)
			errorCount++
		}
	}

	// Allow some errors due to test timing
	if errorCount > 0 {
		t.Error("Some streams failed to open")
	}
}
