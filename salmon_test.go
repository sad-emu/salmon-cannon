package main

import (
	"net"
	"testing"
	"time"
)

func TestSalmonNearFarBridgeRequest(t *testing.T) {
	// Start SalmonFar on a random port
	far, err := NewSalmonFar(0)
	if err != nil {
		t.Fatalf("failed to start SalmonFar: %v", err)
	}
	defer far.ln.Close()

	// Wait a moment for the listener to be ready
	time.Sleep(100 * time.Millisecond)

	// Get the actual port
	port := far.ln.Addr().(*net.TCPAddr).Port

	// Start SalmonNear and connect to far
	near, err := NewSalmonNear("127.0.0.1", port)
	if err != nil {
		t.Fatalf("failed to start SalmonNear: %v", err)
	}
	defer near.conn.Close()

	// If we reach here, the bridge request/response worked
}
