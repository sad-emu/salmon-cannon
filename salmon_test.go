package main

import (
	"net"
	"testing"
	"time"
)

func TestSalmonNearFarBridgeRequest(t *testing.T) {
	// Start SalmonFar on a random port
	far, err := NewSalmonFar(0, []BridgeType{BridgeTCP, BridgeQUIC})
	if err != nil {
		t.Fatalf("failed to start SalmonFar: %v", err)
	}
	defer far.ln.Close()

	// Wait a moment for the listener to be ready
	time.Sleep(100 * time.Millisecond)

	// Get the actual port
	port := far.ln.Addr().(*net.TCPAddr).Port

	// Start SalmonNear and connect to far
	near, err := NewSalmonNear("127.0.0.1", port, []BridgeType{BridgeTCP, BridgeQUIC})
	if err != nil {
		t.Fatalf("failed to start SalmonNear: %v", err)
	}
	defer near.conn.Close()

	// If we reach here, the bridge request/response worked
}

func TestSalmonNearFailFarBridgeRequest(t *testing.T) {
	// Start SalmonFar on a random port
	far, err := NewSalmonFar(0, []BridgeType{BridgeTCP})
	if err != nil {
		t.Fatalf("failed to start SalmonFar: %v", err)
	}
	defer far.ln.Close()

	// Wait a moment for the listener to be ready
	time.Sleep(100 * time.Millisecond)

	// Get the actual port
	port := far.ln.Addr().(*net.TCPAddr).Port

	// Start SalmonNear and connect to far
	near, err := NewSalmonNear("127.0.0.1", port, []BridgeType{BridgeQUIC})
	if err == nil {
		t.Fatalf("Should have failed to start SalmonNear: %v", err)
		defer near.conn.Close()
	}

	// If we reach here, the bridge request/response failed as expected
}
