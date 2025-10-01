package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func startTestHTTPServer(t *testing.T) (addr string, closeFn func()) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	ts := &http.Server{Handler: h}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start test http server: %v", err)
	}
	go ts.Serve(ln)
	return ln.Addr().String(), func() { ts.Close(); ln.Close() }
}

func startTestProxy(t *testing.T) (addr string, closeFn func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start proxy: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleConnection(conn)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestProxyBlocksDirectAndAllowsViaSOCKS(t *testing.T) {
	httpAddr, closeHTTP := startTestHTTPServer(t)
	defer closeHTTP()

	proxyAddr, closeProxy := startTestProxy(t)
	defer closeProxy()

	// Try to connect via the proxy
	resp, err := httpViaSOCKS5(proxyAddr, httpAddr)
	if err != nil {
		t.Fatalf("failed to connect via proxy: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response via proxy: %q", resp)
	}
}

// httpViaSOCKS5 connects to targetAddr via a SOCKS5 proxy at proxyAddr and fetches the root path.
func httpViaSOCKS5(proxyAddr, targetAddr string) (string, error) {
	tr := &http.Transport{
		DialContext: func(_ context.Context, network, addr string) (net.Conn, error) {
			if addr == targetAddr {
				return dialSOCKS5(proxyAddr, addr)
			}
			return net.Dial(network, addr)
		},
	}
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + targetAddr)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b), nil
}

// dialSOCKS5 is a minimal SOCKS5 dialer for testing.
func dialSOCKS5(proxyAddr, destAddr string) (net.Conn, error) {
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		return nil, err
	}
	// handshake
	conn.Write([]byte{0x05, 0x01, 0x00})
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	// connect request
	host, port, _ := net.SplitHostPort(destAddr)
	ip := net.ParseIP(host).To4()
	if ip == nil {
		conn.Close()
		return nil, err
	}
	req := []byte{0x05, 0x01, 0x00, 0x01}
	req = append(req, ip...)
	p, _ := parsePort(port)
	req = append(req, p[0], p[1])
	conn.Write(req)
	resp = make([]byte, 10)
	if _, err := io.ReadFull(conn, resp); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func parsePort(port string) ([2]byte, error) {
	var p [2]byte
	var n int
	_, err := fmt.Sscanf(port, "%d", &n)
	if err != nil {
		return p, err
	}
	p[0] = byte(n >> 8)
	p[1] = byte(n)
	return p, nil
}
