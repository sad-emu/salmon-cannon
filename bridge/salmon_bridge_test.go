package bridge

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"salmoncannon/connections"
	"salmoncannon/status"
	"salmoncannon/utils"
	"testing"
	"time"

	"github.com/sad-emu/anadromous"
)

func TestStatusCheckFailurePreservesConnection(t *testing.T) {
	for _, failure := range []string{"timeout", "invalid ACK"} {
		t.Run(failure, func(t *testing.T) {
			listener, err := anadromous.Listen("127.0.0.1:0", anadromous.WithIdleTimeout(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			name := t.Name()
			addr, err := net.ResolveUDPAddr("udp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			near := NewSalmonBridge(name, "127.0.0.1", addr.Port,
				connections.BridgeNetConfig{IdleTimeout: time.Minute}, nil, "", nil, "")
			serverErrors := make(chan error, 1)
			go func() {
				conn, err := listener.Accept(ctx)
				if err != nil {
					serverErrors <- err
					return
				}
				for probe := 0; ; {
					stream, err := conn.AcceptStream(ctx)
					if err != nil {
						return
					}
					header, err := ReadHeaderType(stream)
					if err != nil {
						serverErrors <- err
						return
					}
					if header == CONNECT_HEADER {
						go func() {
							defer stream.Close()
							io.Copy(stream, stream)
						}()
						continue
					}
					probe++
					if probe == 1 {
						if failure == "invalid ACK" {
							stream.Write([]byte{0xff})
						}
						// Keep this probe unanswered until the near side abandons it.
						go func() {
							defer stream.Close()
							io.Copy(io.Discard, stream)
						}()
						continue
					}
					near.handleStatusPing(stream)
					stream.Close()
				}
			}()

			data, cleanup, err, originalConn := near.transport.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			defer near.transport.CloseConnection(originalConn)
			defer cleanup()
			if _, err := data.Write([]byte{CONNECT_HEADER}); err != nil {
				t.Fatal(err)
			}
			echo := func() {
				t.Helper()
				data.SetDeadline(time.Now().Add(2 * time.Second))
				if _, err := data.Write([]byte("login")); err != nil {
					t.Fatalf("data stream write: %v", err)
				}
				buf := make([]byte, 5)
				if _, err := io.ReadFull(data, buf); err != nil {
					t.Fatalf("data stream read: %v", err)
				}
				if string(buf) != "login" {
					t.Fatalf("unexpected echo: %q", buf)
				}
				data.SetDeadline(time.Time{})
			}
			echo()
			near.StatusCheck()
			// An idle application stream must resume after a failed status probe.
			echo()
			if got := status.GlobalConnMonitorRef.GetStreamCount(name); got != 1 {
				t.Fatalf("failed probe leaked stream count: got %d, want 1", got)
			}
			near.StatusCheck()
			if !status.GlobalConnMonitorRef.GetStatus(name) {
				t.Fatal("subsequent status check did not recover")
			}
			echo()
			stream, done, err, currentConn := near.transport.OpenStream()
			if err != nil {
				t.Fatal(err)
			}
			defer done()
			defer stream.Close()
			if currentConn != originalConn {
				t.Fatal("status check replaced the shared connection")
			}
			select {
			case err := <-serverErrors:
				t.Fatalf("server: %v", err)
			default:
			}
		})
	}
}

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

	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42000
	farBridge := NewSalmonBridge("test1", "", farPort, netCfg,
		nil, "", make([]string, 0), "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, netCfg,
		nil, "", make([]string, 0), "")

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

	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42001

	farBridge := NewSalmonBridge("test2", "", farPort, netCfg,
		nil, "", make([]string, 0), "")
	go func() {
		farBridge.NewFarListen()
	}()
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test2", "127.0.0.1", farPort, netCfg,
		nil, "", make([]string, 0), "")

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

	ln, err := net.Listen("tcp", "127.0.0.1:9993") // random port
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	defer ln.Close()

	go httpServer.Serve(ln)

	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42032
	addressesOut := []string{"127.0.0.1"}

	farBridge := NewSalmonBridge("test9", "127.0.0.1", farPort, netCfg,
		nil, "", addressesOut, "nil")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test9", "127.0.0.1", farPort, netCfg,
		nil, "", make([]string, 0), "nil")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 9993)
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

func TestSalmonBridge_PassFarIpCheckNoEnc(t *testing.T) {
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

	ln, err := net.Listen("tcp", "127.0.0.1:9994") // random port
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	defer ln.Close()

	go httpServer.Serve(ln)

	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42034
	addressesOut := []string{"127.0.0.1"}

	farBridge := NewSalmonBridge("test10", "127.0.0.1", farPort, netCfg,
		nil, "", addressesOut, "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test10", "127.0.0.1", farPort, netCfg,
		nil, "", make([]string, 0), "")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 9994)
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

func TestSalmonBridge_FailFarBridgeIpCheck(t *testing.T) {
	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42000 ///////////////////// Wrong ip so it should fail
	farBridge := NewSalmonBridge("test1", "127.0.0.2", farPort, netCfg, nil,
		"", make([]string, 0), "nil")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test1", "127.0.0.1", farPort, netCfg, nil,
		"", make([]string, 0), "nil")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 1124)
	if err != nil {
		// The far-side rejection may arrive before NewNearConn returns.
		return
	}

	// Wait for conn to fail as the check is AFTER connect
	time.Sleep(700 * time.Millisecond)

	written, werr := conn.Write([]byte("test"))

	if werr == nil || written != 0 {
		t.Fatalf("expected connection to fail far ip check, but it succeeded")
	}

	defer conn.Close()
}

func TestSalmonBridge_FailFarIpFilterCheck(t *testing.T) {
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

	ln, err := net.Listen("tcp", "127.0.0.1:9992") // random port
	if err != nil {
		t.Fatalf("failed to start http server: %v", err)
	}
	defer ln.Close()

	go httpServer.Serve(ln)

	netCfg := connections.BridgeNetConfig{}

	// Far bridge (listener)
	farPort := 42185
	addressesOut := []string{"127.0.0.2"}

	farBridge := NewSalmonBridge("test9", "127.0.0.1", farPort, netCfg,
		nil, "", addressesOut, "")
	go func() {
		farBridge.NewFarListen()
	}()
	// Wait for far to start
	time.Sleep(700 * time.Millisecond)

	// Near bridge (connector)
	nearBridge := NewSalmonBridge("test9", "127.0.0.1", farPort, netCfg,
		nil, "", make([]string, 0), "")

	// Open a connection from near to the HTTP server
	conn, err := nearBridge.NewNearConn("127.0.0.1", 9992)
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
	if _, err := conn.Read(buf); err == nil {
		t.Fatalf("This requiest should have been blocked on the Far IP filter")
	}

	// Verify HTTP server got the request
	select {
	case <-recv:
		t.Fatalf("HTTP server should not have received the request")
	case <-time.After(2 * time.Second):
		// Success
	}
}
