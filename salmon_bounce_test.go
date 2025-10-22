package main

import (
	"net"
	"testing"
	"time"
)

func TestSalmonBounce_BasicUDPForwarding(t *testing.T) {
	// Start a simple UDP echo server (backend)
	backendAddr := "127.0.0.1:0"
	backendConn, err := net.ListenPacket("udp", backendAddr)
	if err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer backendConn.Close()

	backendListenAddr := backendConn.LocalAddr().String()

	// Echo loop for backend
	go func() {
		buf := make([]byte, 65535)
		for {
			n, addr, err := backendConn.ReadFrom(buf)
			if err != nil {
				return
			}
			backendConn.WriteTo(buf[:n], addr)
		}
	}()

	// Start SalmonBounce
	bounceAddr := "127.0.0.1:0"
	routeMap := map[string]string{
		"127.0.0.1": backendListenAddr,
	}

	bounce, err := NewSalmonBounce(bounceAddr, routeMap)
	if err != nil {
		t.Fatalf("failed to create bounce: %v", err)
	}

	if err := bounce.Start(); err != nil {
		t.Fatalf("failed to start bounce: %v", err)
	}
	defer bounce.Stop()

	bounceListenAddr := bounce.listenConn.LocalAddr().String()
	time.Sleep(100 * time.Millisecond) // Let server start

	// Create client and send packet through bounce
	clientConn, err := net.Dial("udp", bounceListenAddr)
	if err != nil {
		t.Fatalf("failed to dial bounce: %v", err)
	}
	defer clientConn.Close()

	testMsg := []byte("hello bounce")
	if _, err := clientConn.Write(testMsg); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Read reply
	buf := make([]byte, 1024)
	clientConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := clientConn.Read(buf)
	if err != nil {
		t.Fatalf("failed to read reply: %v", err)
	}

	if string(buf[:n]) != string(testMsg) {
		t.Fatalf("unexpected reply: got %q, want %q", buf[:n], testMsg)
	}
}

func TestSalmonBounce_SessionCleanup(t *testing.T) {
	backendAddr := "127.0.0.1:0"
	backendConn, err := net.ListenPacket("udp", backendAddr)
	if err != nil {
		t.Fatalf("failed to start backend: %v", err)
	}
	defer backendConn.Close()

	backendListenAddr := backendConn.LocalAddr().String()

	bounceAddr := "127.0.0.1:0"
	routeMap := map[string]string{
		"127.0.0.1": backendListenAddr,
	}

	bounce, err := NewSalmonBounce(bounceAddr, routeMap)
	if err != nil {
		t.Fatalf("failed to create bounce: %v", err)
	}

	if err := bounce.Start(); err != nil {
		t.Fatalf("failed to start bounce: %v", err)
	}
	defer bounce.Stop()

	bounceListenAddr := bounce.listenConn.LocalAddr().String()

	// Create a session
	clientConn, err := net.Dial("udp", bounceListenAddr)
	if err != nil {
		t.Fatalf("failed to dial bounce: %v", err)
	}

	if _, err := clientConn.Write([]byte("test")); err != nil {
		t.Fatalf("failed to write: %v", err)
	}
	clientConn.Close()

	time.Sleep(200 * time.Millisecond)

	// Verify session was created
	bounce.mu.RLock()
	sessionCount := len(bounce.sessions)
	bounce.mu.RUnlock()

	if sessionCount != 1 {
		t.Fatalf("expected 1 session, got %d", sessionCount)
	}
}

func TestSalmonBounce_AddRemoveRoute(t *testing.T) {
	bounce, err := NewSalmonBounce(":0", map[string]string{})
	if err != nil {
		t.Fatalf("failed to create bounce: %v", err)
	}

	bounce.AddRoute("192.168.1.1", "backend1:8080")
	bounce.AddRoute("192.168.1.2", "backend2:8081")

	bounce.mu.RLock()
	if len(bounce.routeMap) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(bounce.routeMap))
	}
	bounce.mu.RUnlock()

	bounce.RemoveRoute("192.168.1.1")

	bounce.mu.RLock()
	if len(bounce.routeMap) != 1 {
		t.Fatalf("expected 1 route after removal, got %d", len(bounce.routeMap))
	}
	bounce.mu.RUnlock()
}
