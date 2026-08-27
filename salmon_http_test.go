package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"salmoncannon/config"
)

type observedProxyRequest struct {
	method             string
	host               string
	body               string
	via                string
	proxyAuthorization string
	hopHeader          string
}

func newDirectHTTPProxy(t *testing.T) *httptest.Server {
	t.Helper()
	near := &SalmonNear{
		bridgeName: "http-test",
		config:     &config.SalmonBridgeConfig{},
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	proxy := newSalmonHTTPProxy(near, dialer.DialContext)
	server := httptest.NewServer(proxy)
	t.Cleanup(func() {
		proxy.transport.CloseIdleConnections()
		server.Close()
	})
	return server
}

func TestHTTPProxyForwardsMethodsBodiesHeadersAndTrailers(t *testing.T) {
	observed := make(chan observedProxyRequest, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observed <- observedProxyRequest{
			method:             r.Method,
			host:               r.Host,
			body:               string(body),
			via:                r.Header.Get("Via"),
			proxyAuthorization: r.Header.Get("Proxy-Authorization"),
			hopHeader:          r.Header.Get("X-Client-Hop"),
		}
		w.Header().Set("Connection", "X-Origin-Hop")
		w.Header().Set("X-Origin-Hop", "remove-me")
		w.Header().Set("X-End-To-End", "keep-me")
		w.Header().Set("Trailer", "X-Origin-Trailer")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("forwarded"))
		w.Header().Set("X-Origin-Trailer", "trailer-value")
	}))
	defer origin.Close()

	proxy := newDirectHTTPProxy(t)
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	req, err := http.NewRequest(http.MethodPatch, origin.URL+"/resource?q=1", strings.NewReader("request-body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Proxy-Authorization", "Basic should-not-reach-origin")
	req.Header.Set("Connection", "X-Client-Hop")
	req.Header.Set("X-Client-Hop", "remove-me")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("proxy request failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}

	if resp.StatusCode != http.StatusCreated || string(body) != "forwarded" {
		t.Fatalf("response = %d %q, want 201 %q", resp.StatusCode, body, "forwarded")
	}
	if got := resp.Header.Get("X-End-To-End"); got != "keep-me" {
		t.Fatalf("end-to-end response header = %q", got)
	}
	if got := resp.Header.Get("X-Origin-Hop"); got != "" {
		t.Fatalf("hop-by-hop response header leaked: %q", got)
	}
	if got := resp.Trailer.Get("X-Origin-Trailer"); got != "trailer-value" {
		t.Fatalf("response trailer = %q", got)
	}

	got := <-observed
	if got.method != http.MethodPatch || got.body != "request-body" {
		t.Fatalf("origin request = %s %q", got.method, got.body)
	}
	originURL, _ := url.Parse(origin.URL)
	if got.host != originURL.Host {
		t.Fatalf("origin Host = %q, want %q", got.host, originURL.Host)
	}
	if !strings.Contains(got.via, "salmon-cannon") {
		t.Fatalf("origin Via = %q", got.via)
	}
	if got.proxyAuthorization != "" || got.hopHeader != "" {
		t.Fatalf("proxy-only headers leaked: auth=%q hop=%q", got.proxyAuthorization, got.hopHeader)
	}
}

func TestHTTPProxyReusesPersistentOriginConnections(t *testing.T) {
	remoteAddresses := make(chan string, 2)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remoteAddresses <- r.RemoteAddr
		_, _ = io.WriteString(w, "ok")
	}))
	defer origin.Close()

	proxy := newDirectHTTPProxy(t)
	proxyURL, _ := url.Parse(proxy.URL)
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	for i := 0; i < 2; i++ {
		resp, err := client.Get(origin.URL + "/persistent")
		if err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		_, readErr := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read response %d: %v", i+1, readErr)
		}
	}

	first, second := <-remoteAddresses, <-remoteAddresses
	if first != second {
		t.Fatalf("origin connection was not reused: %q then %q", first, second)
	}
}

func TestHTTPProxyConnectPreservesBufferedTunnelBytes(t *testing.T) {
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echoListener.Close()
	go func() {
		conn, acceptErr := echoListener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	proxy := newDirectHTTPProxy(t)
	proxyURL, _ := url.Parse(proxy.URL)
	client, err := net.DialTimeout("tcp", proxyURL.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	payload := "buffered-after-connect"
	request := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", echoListener.Addr(), echoListener.Addr(), payload)
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusLine, " 200 ") {
		t.Fatalf("CONNECT status = %q", statusLine)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\r\n" {
			break
		}
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read CONNECT echo: %v", err)
	}
	if string(echo) != payload {
		t.Fatalf("CONNECT echo = %q, want %q", echo, payload)
	}
}

func TestHTTPProxyRelaysProtocolUpgrade(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !headerHasToken(r.Header, "Connection", "upgrade") || r.Header.Get("Upgrade") != "test-echo" {
			http.Error(w, "missing upgrade", http.StatusBadRequest)
			return
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "no hijacker", http.StatusInternalServerError)
			return
		}
		conn, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = buffered.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: test-echo\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(conn, buffered)
	}))
	defer origin.Close()

	proxy := newDirectHTTPProxy(t)
	proxyURL, _ := url.Parse(proxy.URL)
	originURL, _ := url.Parse(origin.URL)
	client, err := net.DialTimeout("tcp", proxyURL.Host, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))

	request := fmt.Sprintf("GET %s/upgrade HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: test-echo\r\n\r\n", origin.URL, originURL.Host)
	if _, err := io.WriteString(client, request); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusLine, " 101 ") {
		t.Fatalf("upgrade status = %q", statusLine)
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\r\n" {
			break
		}
	}

	const payload = "ping"
	if _, err := io.WriteString(client, payload); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, echo); err != nil {
		t.Fatalf("read upgraded echo: %v", err)
	}
	if string(echo) != payload {
		t.Fatalf("upgrade echo = %q, want %q", echo, payload)
	}
}

func TestHTTPProxyRejectsUnsupportedScheme(t *testing.T) {
	near := &SalmonNear{config: &config.SalmonBridgeConfig{}}
	dialer := &net.Dialer{}
	proxy := newSalmonHTTPProxy(near, dialer.DialContext)
	req := httptest.NewRequest(http.MethodGet, "ftp://example.com/file", nil)
	recorder := httptest.NewRecorder()

	proxy.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
}

func TestNormalizeProxyAuthority(t *testing.T) {
	tests := map[string]string{
		"example.com":        "example.com:443",
		"example.com:8443":   "example.com:8443",
		"[2001:db8::1]":      "[2001:db8::1]:443",
		"[2001:db8::1]:8443": "[2001:db8::1]:8443",
	}
	for input, want := range tests {
		got, err := normalizeProxyAuthority(input, "443")
		if err != nil {
			t.Errorf("normalize %q: %v", input, err)
			continue
		}
		if got != want {
			t.Errorf("normalize %q = %q, want %q", input, got, want)
		}
	}
	if _, err := normalizeProxyAuthority("2001:db8::1", "443"); err == nil {
		t.Fatal("unbracketed IPv6 target was accepted")
	}
}
