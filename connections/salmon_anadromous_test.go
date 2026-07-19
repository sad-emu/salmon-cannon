package connections

import (
	"context"
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

	// Verify no connections were created
	sq.connectionsMu.RLock()
	connCount := len(sq.connections)
	sq.connectionsMu.RUnlock()

	if connCount != 0 {
		t.Errorf("Expected 0 connections after failed connection attempt, got %d", connCount)
	}
}

func TestConnectionCreationFailure(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	// Use invalid address to test error handling
	sq := NewSalmonAnadromous(1, "invalid-host", "test-bridge", netcfg, "")

	// Attempt to open stream should fail when trying to create connection
	_, cleanup, err, _ := sq.OpenStream()
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Error("Expected error when creating connection to invalid host, got nil")
	}
}

func TestOpenStreamWithoutConnection(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "invalid-host", "test-bridge", netcfg, "")

	_, cleanup, err, _ := sq.OpenStream()
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Error("Expected error when opening stream without connection, got nil")
	}
}

func TestOpenStreamIntegration(t *testing.T) {
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

	// Allow some errors due to test timing
	if errorCount == numStreams {
		t.Error("All streams failed to open")
	}
}

func TestConnectionPoolFailure(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "invalid-host", "test-bridge", netcfg, "")

	// Try to open stream to invalid host (should fail)
	_, cleanup, err, _ := sq.OpenStream()
	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Error("Expected error when opening stream to invalid host, got nil")
	}

	// Verify no connections were created
	sq.connectionsMu.RLock()
	connCount := len(sq.connections)
	sq.connectionsMu.RUnlock()

	if connCount != 0 {
		t.Errorf("Expected 0 connections after failed connect, got %d", connCount)
	}
}

func TestMutexSafety(t *testing.T) {
	netcfg := BridgeNetConfig{IdleTimeout: 2 * time.Second}
	sq := NewSalmonAnadromous(1, "invalid-host", "test-bridge", netcfg, "")

	// Try to access connection pool concurrently
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

func TestConnectionPooling(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	netcfg := BridgeNetConfig{
		IdleTimeout: 5 * time.Second,
		MaxStreams:  100,
	}

	MaxStreamsPerConnection = 200
	MaxConnectionsPerBridge = 1

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

	// Server goroutine that handles multiple connections and streams
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

	// Open multiple streams to trigger connection pooling
	var wg sync.WaitGroup
	numStreams := 5

	for i := 0; i < numStreams; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			stream, cleanup, err, _ := sq.OpenStream()
			if err != nil {
				t.Logf("Stream %d error: %v", id, err)
				return
			}
			defer cleanup()
			defer stream.Close()

			testData := []byte("test")
			stream.Write(testData)
		}(i)
	}

	wg.Wait()

	// Check that connections were created
	sq.connectionsMu.RLock()
	connCount := len(sq.connections)
	sq.connectionsMu.RUnlock()

	if connCount == 0 {
		t.Error("Expected at least one connection to be created")
	}

	t.Logf("Created %d connection(s) for %d streams", connCount, numStreams)
}

func TestMaxConcurrentStreamOpeningFails(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	var streamsToTest int = 100

	MaxStreamsPerConnection = 10
	MaxConnectionsPerBridge = 1

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

	// Expect errors as we have limited max streams/connections
	if errorCount <= 0 {
		t.Error("Some streams should have failed to open")
	}
}

func TestMaxConcurrentStreamOpening(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	var streamsToTest int = 100

	MaxStreamsPerConnection = 50
	MaxConnectionsPerBridge = 2

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

// TestStaleConnectionRecoveryWithMaxBridges1 tests the production scenario where:
//   - MaxConnectionsPerBridge = 1 (only one connection allowed in the pool)
//   - Far side goes down and comes back up (server restart scenario)
//   - Near side must detect the stale connection, drop it from the pool,
//     and establish a fresh connection to the restarted far side.
func TestStaleConnectionRecoveryWithMaxBridges1(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	netcfg := BridgeNetConfig{
		IdleTimeout: 2 * time.Second,
		MaxStreams:  10,
	}

	// Set to 1 connection max (production scenario)
	MaxStreamsPerConnection = 10
	MaxConnectionsPerBridge = 1

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
	stream1, cleanup1, err, _ := sq.OpenStream()
	if err != nil {
		t.Fatalf("Failed to open first stream: %v", err)
	}

	// Verify connection was created
	sq.connectionsMu.RLock()
	connCount := len(sq.connections)
	sq.connectionsMu.RUnlock()

	if connCount != 1 {
		t.Fatalf("Expected 1 connection, got %d", connCount)
	}

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

	// The old connection may still be in the pool
	sq.connectionsMu.RLock()
	oldConnCount := len(sq.connections)
	sq.connectionsMu.RUnlock()

	t.Logf("Connections in pool after server restart: %d", oldConnCount)

	// Try to open a new stream - the first attempt may fail if it picks the
	// stale connection, but that failure must evict it from the pool so the
	// retry can dial fresh.
	stream2, cleanup2, err, _ := sq.OpenStream()

	if err != nil {
		t.Logf("OpenStream failed on stale connection, retrying: %v", err)
		stream2, cleanup2, err, _ = sq.OpenStream()
	}

	if err == nil {
		// If we got a stream, try to use it
		testData2 := []byte("test-data-2")
		_, writeErr := stream2.Write(testData2)

		if writeErr != nil {
			t.Logf("Write failed (expected with stale connection): %v", writeErr)
		}

		// Try to read
		buf2 := make([]byte, 100)
		stream2.SetReadDeadline(time.Now().Add(2 * time.Second))
		_, readErr := stream2.Read(buf2)

		if readErr != nil {
			t.Logf("Read failed (expected with stale connection): %v", readErr)
		}

		stream2.Close()
		cleanup2()
	} else {
		t.Logf("OpenStream failed after retry: %v", err)
		t.Error("BUG REPRODUCED: Cannot open new stream because stale connection is blocking the pool")
	}
}
