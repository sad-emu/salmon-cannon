package bridge

import (
	"crypto/tls"
	"net"
	"net/http"
	"salmoncannon/utils"
	"testing"
	"time"

	quic "github.com/quic-go/quic-go"
)

func TestSalmonBridge_HTTPProxyEndToEnd(t *testing.T) {
	// Start a simple HTTP server
	recv := make(chan struct{}, 1) // buffered so handler doesn't block

	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/test" {
				recv <- struct{}{}
			}
			w.WriteHeader(200)
			w.Write([]byte("ok"))
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:1099") // random port
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	defer ln.Close()

	go httpServer.Serve(ln)

	// TLS and QUIC config
	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test1"},
		Certificates: []tls.Certificate{utils.GenerateSelfSignedCert()}}
	quicCfg := &quic.Config{EnableDatagrams: false}

	// Far bridge (listener)
	farPort := 42000
	farBridge := NewSalmonBridge("test1", "", farPort, tlsCfg, quicCfg, nil, false, "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, tlsCfg, quicCfg, nil, true, "")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 1099)
	if err != nil {
		t.Fatalf("near bridge failed: %v", err)
	}
	defer conn.Close()

	// Send HTTP request manually
	req := "GET /test HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	// Read response
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// Verify HTTP server got the request
	select {
	case <-recv:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatalf("HTTP server did not receive request")
	}
}

func TestSalmonBridge_HTTPSProxyEndToEnd(t *testing.T) {
	recv := make(chan struct{}, 1) // buffered so handler doesn't block

	// Generate self-signed certificate
	cert := utils.GenerateSelfSignedCert()
	tlsCfg := &tls.Config{
		Certificates:       []tls.Certificate{cert},
		InsecureSkipVerify: true, // for client below
		ServerName:         "127.0.0.1",
	}

	// Start HTTPS server
	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/test2" {
				recv <- struct{}{}
			}
			w.WriteHeader(200)
			w.Write([]byte("ok"))
		}),
	}

	ln, err := tls.Listen("tcp", "127.0.0.1:1098", tlsCfg)
	if err != nil {
		t.Fatalf("failed to start HTTPS server: %v", err)
	}
	defer ln.Close()
	go httpServer.Serve(ln)

	// TLS and QUIC config for bridges
	quicCfg := &quic.Config{EnableDatagrams: false}

	// Far bridge (listener)
	farPort := 42001
	bridgeTLSCfg := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"test2"},
		Certificates:       []tls.Certificate{utils.GenerateSelfSignedCert()},
	}

	farBridge := NewSalmonBridge("test2", "", farPort, bridgeTLSCfg, quicCfg, nil, false, "")
	go func() {
		farBridge.NewFarListen()
	}()
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test2", "127.0.0.1", farPort, bridgeTLSCfg, quicCfg, nil, true, "")

	// Open a connection from near to the HTTPS server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 1098)
	if err != nil {
		t.Fatalf("near bridge failed: %v", err)
	}
	defer conn.Close()

	tlsConn := tls.Client(conn, tlsCfg)
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS handshake failed: %v", err)
	}

	req := "GET /test2 HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	if _, err := tlsConn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	if _, err := tlsConn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}
	buf := make([]byte, 1024)
	tlsConn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := tlsConn.Read(buf); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// Confirm HTTPS server got the request
	select {
	case <-recv:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatalf("HTTPS server did not receive request")
	}
}

func TestSalmonBridge_PassFarIpCheck(t *testing.T) {
	// Start a simple HTTP server
	recv := make(chan struct{}, 1) // buffered so handler doesn't block

	httpServer := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/test" {
				recv <- struct{}{}
			}
			w.WriteHeader(200)
			w.Write([]byte("ok"))
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:1123") // random port
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	defer ln.Close()

	go httpServer.Serve(ln)

	// TLS and QUIC config
	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test1"},
		Certificates: []tls.Certificate{utils.GenerateSelfSignedCert()}}
	quicCfg := &quic.Config{EnableDatagrams: false}

	// Far bridge (listener)
	farPort := 42000
	farBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, tlsCfg, quicCfg, nil, false, "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, tlsCfg, quicCfg, nil, true, "")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 1123)
	if err != nil {
		t.Fatalf("near bridge failed: %v", err)
	}
	defer conn.Close()

	// Send HTTP request manually
	req := "GET /test HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write request: %v", err)
	}

	// Read response
	buf := make([]byte, 1024)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Read(buf); err != nil {
		t.Fatalf("failed to read response: %v", err)
	}

	// Verify HTTP server got the request
	select {
	case <-recv:
		// Success
	case <-time.After(2 * time.Second):
		t.Fatalf("HTTP server did not receive request")
	}
}

func TestSalmonBridge_FailFarIpCheck(t *testing.T) {
	// TLS and QUIC config
	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test1"},
		Certificates: []tls.Certificate{utils.GenerateSelfSignedCert()}}
	quicCfg := &quic.Config{EnableDatagrams: false}

	// Far bridge (listener)
	farPort := 42000 ///////////////////// Wrong ip so it should fail
	farBridge := NewSalmonBridge("test1", "127.0.0.2", farPort, tlsCfg, quicCfg, nil, false, "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, tlsCfg, quicCfg, nil, true, "")

	// Open a connection from near to the HTTP server
	conn, _ := nearBridge.NewNearConn("127.0.0.1", 1124)

	// Wait for conn to fail as the check is AFTER connect
	time.Sleep(700 * time.Millisecond)

	written, werr := conn.Write([]byte("test"))

	if werr == nil || written != 0 {
		t.Fatalf("expected connection to fail far ip check, but it succeeded")
	}

	defer conn.Close()
}
