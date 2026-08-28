package connections

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sad-emu/anadromous"
)

func TestNewSalmonAnadromous(t *testing.T) {
	sq := NewSalmonAnadromous(8080, "127.0.0.1", "test-bridge", BridgeNetConfig{}, "")

	if sq == nil {
		t.Fatal("NewSalmonAnadromous returned nil")
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

	if sq.interfaceName != "" {
		t.Errorf("Expected empty interfaceName, got %s", sq.interfaceName)
	}
}

func TestNewSalmonAnadromousWithInterface(t *testing.T) {
	sq := NewSalmonAnadromous(8080, "127.0.0.1", "test-bridge", BridgeNetConfig{}, "eth0")

	if sq.interfaceName != "eth0" {
		t.Errorf("Expected interfaceName eth0, got %s", sq.interfaceName)
	}
}

func TestNewSalmonAnadromousOwnsOneWirePacer(t *testing.T) {
	netcfg := BridgeNetConfig{
		BandwidthLimit:            1_250_000_000,
		PacingDatagramOverhead:    66,
		PacingMinimumDatagramSize: 84,
		PacingBurstBytes:          2_500_000,
		TransportBatchSize:        32,
	}
	sq := NewSalmonAnadromous(8080, "127.0.0.1", "test-bridge", netcfg, "")
	if sq.wirePacer == nil {
		t.Fatal("bandwidth-limited bridge did not create a shared wire pacer")
	}
	if got := sq.wirePacer.RateBytesPerSecond(); got != 1_250_000_000 {
		t.Fatalf("pacer rate = %d, want 1250000000", got)
	}
	if got := sq.wirePacer.BurstBytes(); got != 2_500_000 {
		t.Fatalf("pacer burst = %d, want 2500000", got)
	}
	accounting := sq.wirePacer.Accounting()
	if accounting.PerDatagramOverhead != 66 || accounting.MinimumDatagramSize != 84 {
		t.Fatalf("pacer accounting = %+v, want overhead 66/minimum 84", accounting)
	}
	if got := len(sq.opts); got != 2 {
		t.Fatalf("wire pacer + batch config produced %d options, want 2", got)
	}

	unlimited := NewSalmonAnadromous(8080, "127.0.0.1", "unlimited", BridgeNetConfig{}, "")
	if unlimited.wirePacer != nil {
		t.Fatal("unlimited bridge unexpectedly created a wire pacer")
	}
}

func TestBridgeNetConfigFECOptions(t *testing.T) {
	fecGroup := 8
	if got := len((BridgeNetConfig{}).options("")); got != 0 {
		t.Fatalf("empty config produced %d options, want 0", got)
	}

	configured := BridgeNetConfig{FECGroupSize: &fecGroup, FEC2D: true}
	if got := len(configured.options("")); got != 2 {
		t.Fatalf("FEC and FEC2D config produced %d options, want 2", got)
	}

	disabled := 0
	if got := len((BridgeNetConfig{FECGroupSize: &disabled}).options("")); got != 1 {
		t.Fatalf("explicit FEC disable produced %d options, want 1", got)
	}
}

func TestBridgeNetConfigMaxBytesInFlightOption(t *testing.T) {
	if got := len((BridgeNetConfig{MaxBytesInFlight: 32 << 20}).options("")); got != 1 {
		t.Fatalf("MaxBytesInFlight config produced %d options, want 1", got)
	}
	if got := len((BridgeNetConfig{MaxBytesInFlight: 0}).options("")); got != 0 {
		t.Fatalf("zero MaxBytesInFlight produced %d options, want derived protocol default", got)
	}
}

func TestBridgeNetConfigRetransmitTimeoutOptions(t *testing.T) {
	configured := BridgeNetConfig{
		InitialRetransmitTimeout: 300 * time.Millisecond,
		MinRetransmitTimeout:     150 * time.Millisecond,
	}
	if got := len(configured.options("")); got != 2 {
		t.Fatalf("retransmit timeout config produced %d options, want 2", got)
	}
	if got := len((BridgeNetConfig{}).options("")); got != 0 {
		t.Fatalf("zero retransmit timeouts produced %d options, want protocol defaults", got)
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

func TestConnectionToInvalidAddress(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "invalid-host-name-that-does-not-exist", "test-bridge", netcfg, "")

	// Try to open a stream, which will attempt to create a connection
	_, cleanup, err, _ := sq.OpenStream()
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Error("Expected error when connecting to invalid host, got nil")
	}

	sq.connectionMu.Lock()
	defer sq.connectionMu.Unlock()
	if sq.connection != nil {
		t.Error("Expected no connection after failed connection attempt")
	}
}

func TestOpenStreamIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	fecGroup := 8
	netcfg := BridgeNetConfig{
		IdleTimeout:  2 * time.Second,
		MaxStreams:   100,
		FECGroupSize: &fecGroup,
		FEC2D:        true,
	}

	// Start server
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
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
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Open stream
	stream, cleanup, err, _ := sq.OpenStream()
	if err != nil {
		t.Fatalf("Failed to open stream: %v", err)
	}
	defer cleanup()
	defer stream.Close()

	// Test writing and reading
	testData := []byte("hello anadromous")
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

	netcfg := BridgeNetConfig{
		IdleTimeout: 2 * time.Second,
		MaxStreams:  100,
	}

	// Start server
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
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
		for {
			stream, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go func(s *anadromous.Stream) {
				defer s.Close()
				buf := make([]byte, 100)
				n, _ := s.Read(buf)
				s.Write(buf[:n])
			}(stream)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Open multiple streams concurrently
	var wg sync.WaitGroup
	numStreams := 10
	errors := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, cleanup, err, _ := sq.OpenStream()
			if err != nil {
				errors <- err
				return
			}
			defer cleanup()
			defer stream.Close()

			// Write and read
			testData := []byte("test")
			stream.Write(testData)
			buf := make([]byte, 100)
			stream.SetReadDeadline(time.Now().Add(2 * time.Second))
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

	if errorCount != 0 {
		t.Fatalf("%d of %d within-limit streams failed to open", errorCount, numStreams)
	}
}

func TestMutexSafety(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "invalid-host", "test-bridge", netcfg, "")

	// Try to initialize the one connection concurrently.
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cleanup, _, _ := sq.OpenStream()
			if cleanup != nil {
				cleanup()
			}
		}()
	}

	wg.Wait()
	// Test passes if no race condition detected
}

func TestOpenStreamInvalidInterface(t *testing.T) {
	// Binding to a non-existent interface should fail when dialing
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "127.0.0.1", "test-bridge", netcfg, "nonexistent-interface-12345")

	_, cleanup, err, _ := sq.OpenStream()
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Error("Expected error when binding to non-existent interface")
	}

	if err != nil {
		// Just check that we got an error, the exact message may vary by platform
		t.Logf("Got expected error: %v", err)
	}
}

func TestSingleConnectionMultiplexesStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	netcfg := BridgeNetConfig{
		IdleTimeout: 5 * time.Second,
		MaxStreams:  100,
	}

	// Start server
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// The server continues accepting so the test can detect an accidental
	// second transport connection.
	acceptedConnections := make(chan struct{}, 2)
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			acceptedConnections <- struct{}{}
			go func(c *anadromous.Connection) {
				defer c.CloseWithError(0, "test done")
				for {
					stream, err := c.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func(s *anadromous.Stream) {
						defer s.Close()
						buf := make([]byte, 100)
						n, _ := s.Read(buf)
						s.Write(buf[:n])
					}(stream)
				}
			}(conn)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Open multiple streams concurrently on the bridge connection.
	var wg sync.WaitGroup
	numStreams := 5
	connectionsUsed := make(chan *anadromous.Connection, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, cleanup, err, conn := sq.OpenStream()
			if err != nil {
				t.Errorf("stream %d: %v", id, err)
				return
			}
			connectionsUsed <- conn
			defer cleanup()
			defer stream.Close()

			testData := []byte("test")
			stream.Write(testData)
		}(i)
	}

	wg.Wait()

	close(connectionsUsed)
	var first *anadromous.Connection
	for conn := range connectionsUsed {
		if first == nil {
			first = conn
			continue
		}
		if conn != first {
			t.Fatal("streams used more than one transport connection")
		}
	}

	select {
	case <-acceptedConnections:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not accept the transport connection")
	}
	select {
	case <-acceptedConnections:
		t.Fatal("server accepted more than one transport connection")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestMaxStreamsDoesNotCreateSecondConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	netcfg := BridgeNetConfig{
		IdleTimeout: 2 * time.Second,
		MaxStreams:  2,
	}
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("start listener: %v", err)
	}
	defer listener.Close()

	accepted := make(chan *anadromous.Connection, 2)
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()

	udpAddr, err := net.ResolveUDPAddr("udp", listener.Addr().String())
	if err != nil {
		t.Fatalf("resolve listener address: %v", err)
	}
	sq := NewSalmonAnadromous(udpAddr.Port, "127.0.0.1", "capacity-test", netcfg, "")

	stream1, cleanup1, err, conn1 := sq.OpenStream()
	if err != nil {
		t.Fatalf("open first stream: %v", err)
	}
	defer cleanup1()
	defer stream1.CancelRead(0)
	defer stream1.CancelWrite(0)

	stream2, cleanup2, err, conn2 := sq.OpenStream()
	if err != nil {
		t.Fatalf("open second stream: %v", err)
	}
	defer cleanup2()
	defer stream2.CancelRead(0)
	defer stream2.CancelWrite(0)
	if conn2 != conn1 {
		t.Fatal("streams were opened on different transport connections")
	}

	if _, _, err, _ := sq.OpenStream(); !errors.Is(err, anadromous.ErrMaxStreams) {
		t.Fatalf("third stream error = %v, want %v", err, anadromous.ErrMaxStreams)
	}

	select {
	case serverConn := <-accepted:
		defer serverConn.CloseWithError(0, "test done")
	case <-time.After(2 * time.Second):
		t.Fatal("server did not accept the transport connection")
	}
	select {
	case second := <-accepted:
		second.CloseWithError(0, "unexpected second connection")
		t.Fatal("stream saturation created a second transport connection")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestConcurrentOpenHonorsMaxStreams(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	var streamsToTest int = 100

	netcfg := BridgeNetConfig{
		IdleTimeout: 1 * time.Second,
		MaxStreams:  10,
	}

	// Start server
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
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
		for {
			stream, err := conn.AcceptStream(context.Background())
			if err != nil {
				return
			}
			go func(s *anadromous.Stream) {
				defer s.Close()
				buf := make([]byte, 100)
				n, _ := s.Read(buf)
				s.Write(buf[:n])
			}(stream)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Open multiple streams concurrently
	var wg sync.WaitGroup
	numStreams := streamsToTest
	errors := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, cleanup, err, _ := sq.OpenStream()
			if err != nil {
				errors <- err
				return
			}
			defer cleanup()
			defer stream.Close()

			// Write and read
			testData := []byte("test")
			stream.Write(testData)
			buf := make([]byte, 100)
			stream.SetReadDeadline(time.Now().Add(2 * time.Second))
			stream.Read(buf)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		if err != nil {
			//t.Logf("Stream error: %v", err)
			errorCount++
		}
	}

	// Expect errors because one connection is limited to 10 concurrent streams.
	if errorCount <= 0 {
		t.Error("Some streams should have failed to open")
	}
}

func TestMaxConcurrentStreamsUseSingleConnection(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	var streamsToTest int = 100

	netcfg := BridgeNetConfig{
		IdleTimeout: 1 * time.Second,
		MaxStreams:  streamsToTest,
	}

	// Start server
	listener, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
	}
	defer listener.Close()

	serverAddr := listener.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// Server goroutine that handles multiple streams
	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			go func(c *anadromous.Connection) {
				defer c.CloseWithError(0, "test done")
				for {
					stream, err := c.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func(s *anadromous.Stream) {
						defer s.Close()
						buf := make([]byte, 100)
						n, _ := s.Read(buf)
						s.Write(buf[:n])
					}(stream)
				}
			}(conn)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Open multiple streams concurrently
	var wg sync.WaitGroup
	numStreams := streamsToTest
	errors := make(chan error, numStreams)

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, cleanup, err, _ := sq.OpenStream()
			if err != nil {
				errors <- err
				return
			}
			defer cleanup()
			defer stream.Close()

			// Write and read
			testData := []byte("test")
			stream.Write(testData)
			buf := make([]byte, 100)
			stream.SetReadDeadline(time.Now().Add(2 * time.Second))
			stream.Read(buf)
		}(i)
	}

	wg.Wait()
	close(errors)

	// Check for errors
	errorCount := 0
	for err := range errors {
		if err != nil {
			//t.Logf("Stream error: %v", err)
			errorCount++
		}
	}

	// Allow some errors due to test timing
	if errorCount > 0 {
		t.Error("Some streams have failed to open")
	}
}

// TestStaleConnectionRecovery verifies that a dead bridge connection is
// discarded and replaced after the far side restarts.
func TestStaleConnectionRecovery(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	netcfg := BridgeNetConfig{
		IdleTimeout: 2 * time.Second,
		MaxStreams:  10,
	}

	// Start first server
	listener1, err := anadromous.Listen("127.0.0.1:0", netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start anadromous listener: %v", err)
	}

	serverAddr := listener1.Addr().String()
	var port int
	if addr, err := net.ResolveUDPAddr("udp", serverAddr); err == nil {
		port = addr.Port
	}

	// Server goroutine that accepts one connection and handles streams
	serverCtx, serverCancel := context.WithCancel(context.Background())
	serverDone := make(chan struct{})

	go func() {
		defer close(serverDone)
		conn, err := listener1.Accept(serverCtx)
		if err != nil {
			return
		}
		defer conn.CloseWithError(0, "test done")

		for {
			stream, err := conn.AcceptStream(serverCtx)
			if err != nil {
				return
			}
			go func(s *anadromous.Stream) {
				defer s.Close()
				buf := make([]byte, 100)
				n, _ := s.Read(buf)
				s.Write(buf[:n])
			}(stream)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Create client
	sq := NewSalmonAnadromous(port, "127.0.0.1", "test-bridge", netcfg, "")

	// Successfully open a stream to establish connection
	stream1, cleanup1, err, firstConnection := sq.OpenStream()
	if err != nil {
		t.Fatalf("Failed to open first stream: %v", err)
	}

	if firstConnection == nil {
		t.Fatal("expected the first bridge connection to be created")
	}
	sq.connectionMu.Lock()
	if sq.connection != firstConnection {
		sq.connectionMu.Unlock()
		t.Fatal("first stream was not opened on the bridge connection")
	}
	sq.connectionMu.Unlock()

	// Write some data to confirm it works
	testData := []byte("test-data-1")
	_, err = stream1.Write(testData)
	if err != nil {
		t.Fatalf("Failed to write to stream: %v", err)
	}

	buf := make([]byte, 100)
	stream1.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, err := stream1.Read(buf)
	if err != nil && err.Error() != "EOF" {
		t.Logf("Read from stream got error (may be timing): %v", err)
	}

	if n > 0 && string(buf[:n]) != string(testData) {
		t.Errorf("Expected %s, got %s", testData, buf[:n])
	}

	stream1.Close()
	cleanup1()

	t.Log("First connection successful") // Now simulate the far side going down
	serverCancel()
	listener1.Close()

	// Wait for server to shut down
	select {
	case <-serverDone:
		t.Log("First server shut down")
	case <-time.After(2 * time.Second):
		t.Fatal("Server didn't shut down in time")
	}

	// Wait long enough for the client idle timeout to kill the connection
	time.Sleep(500 * time.Millisecond)

	// Start a new server on the same port (simulating far side coming back)
	listener2, err := anadromous.Listen(serverAddr, netcfg.options("")...)
	if err != nil {
		t.Fatalf("Failed to start second anadromous listener: %v", err)
	}
	defer listener2.Close()

	serverCtx2, serverCancel2 := context.WithCancel(context.Background())
	defer serverCancel2()

	go func() {
		conn, err := listener2.Accept(serverCtx2)
		if err != nil {
			return
		}
		defer conn.CloseWithError(0, "test done")

		for {
			stream, err := conn.AcceptStream(serverCtx2)
			if err != nil {
				return
			}
			go func(s *anadromous.Stream) {
				defer s.Close()
				buf := make([]byte, 100)
				n, _ := s.Read(buf)
				s.Write(buf[:n])
			}(stream)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	t.Log("Second server started")

	// The first attempt may observe the stale connection. That failure must
	// discard it so a retry can dial the restarted far side.
	stream2, cleanup2, err, secondConnection := sq.OpenStream()

	if err != nil {
		t.Logf("OpenStream failed on stale connection, retrying: %v", err)
		stream2, cleanup2, err, secondConnection = sq.OpenStream()
	}

	if err != nil {
		t.Fatalf("open stream after far-side restart: %v", err)
	}
	defer cleanup2()
	defer stream2.Close()
	if secondConnection == firstConnection {
		t.Fatal("stale bridge connection was not replaced")
	}

	testData2 := []byte("test-data-2")
	if _, err := stream2.Write(testData2); err != nil {
		t.Fatalf("write through replacement connection: %v", err)
	}
	buf2 := make([]byte, len(testData2))
	stream2.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = stream2.Read(buf2)
	if err != nil {
		t.Fatalf("read through replacement connection: %v", err)
	}
	if string(buf2[:n]) != string(testData2) {
		t.Fatalf("replacement echo = %q, want %q", buf2[:n], testData2)
	}
}
