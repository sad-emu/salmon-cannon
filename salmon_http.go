package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"salmoncannon/config"
	"salmoncannon/status"
)

const (
	httpProxyReadHeaderTimeout = 15 * time.Second
	httpProxyMaxHeaderBytes    = 1 << 20
)

type httpProxyDialContext func(context.Context, string, string) (net.Conn, error)

// salmonHTTPProxy is an HTTP/1.x forward proxy. Its outbound transport dials
// through the selected Salmon bridge, so ordinary HTTP requests, HTTPS
// CONNECT tunnels, and protocol upgrades all use the same near/far path.
type salmonHTTPProxy struct {
	near        *SalmonNear
	dialContext httpProxyDialContext
	transport   *http.Transport
}

func newSalmonHTTPProxy(near *SalmonNear, dialContext httpProxyDialContext) *salmonHTTPProxy {
	p := &salmonHTTPProxy{near: near}
	if dialContext == nil {
		dialContext = p.dialThroughBridge
	}
	p.dialContext = dialContext

	idleTimeout := 90 * time.Second
	if near != nil && near.config != nil && near.config.IdleTimeout.Duration() > 0 {
		idleTimeout = near.config.IdleTimeout.Duration()
	}
	p.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           p.dialContext,
		ForceAttemptHTTP2:     true,
		DisableCompression:    true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       idleTimeout,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
	return p
}

func initHTTPNear(cfg *config.SalmonBridgeConfig, near *SalmonNear) {
	if cfg.HttpListenPort <= 0 {
		return
	}

	addr := net.JoinHostPort(cfg.SocksListenAddress, strconv.Itoa(cfg.HttpListenPort))
	log.Printf("NEAR: Initializing HTTP proxy listener for bridge %s on %s", cfg.Name, addr)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("NEAR: Failed to listen HTTP on %s: %v", addr, err)
	}
	log.Printf("NEAR: HTTP proxy listening on %s", addr)

	proxy := near.httpProxy
	if proxy == nil {
		proxy = newSalmonHTTPProxy(near, nil)
		near.httpProxy = proxy
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           proxy,
		ReadHeaderTimeout: httpProxyReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout.Duration(),
		MaxHeaderBytes:    httpProxyMaxHeaderBytes,
	}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Printf("NEAR: HTTP proxy on %s stopped: %v", addr, err)
	}
}

func (p *salmonHTTPProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status.GlobalConnMonitorRef.IncHTTP()
	defer status.GlobalConnMonitorRef.DecHTTP()

	if p.near != nil && p.near.config != nil && p.near.shouldBlockNearConn(r.RemoteAddr) {
		log.Printf("NEAR: Bridge %s rejected HTTP client IP: %s", p.near.bridgeName, r.RemoteAddr)
		http.Error(w, "proxy client is not allowed", http.StatusForbidden)
		return
	}

	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleForward(w, r)
}

func (p *salmonHTTPProxy) dialThroughBridge(ctx context.Context, network, address string) (net.Conn, error) {
	if p.near == nil || p.near.currentBridge == nil {
		return nil, fmt.Errorf("HTTP proxy has no Salmon bridge")
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("HTTP proxy does not support network %q", network)
	}

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("invalid upstream address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid upstream port %q", portText)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	conn, err := p.near.currentBridge.NewNearConn(host, port)
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		conn.Close()
		return nil, ctx.Err()
	default:
		return conn, nil
	}
}

func (p *salmonHTTPProxy) handleForward(w http.ResponseWriter, r *http.Request) {
	outReq, requestUpgrade, err := outboundProxyRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		log.Printf("NEAR: HTTP proxy request to %s failed: %v", outReq.URL.Redacted(), err)
		http.Error(w, "upstream request failed", http.StatusBadGateway)
		return
	}
	if resp.Body == nil {
		resp.Body = http.NoBody
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusSwitchingProtocols {
		p.handleUpgrade(w, resp, requestUpgrade)
		return
	}

	removeHopByHopHeaders(resp.Header)
	appendVia(resp.Header, resp.ProtoMajor, resp.ProtoMinor)
	copyHTTPHeader(w.Header(), resp.Header)
	for key := range resp.Trailer {
		w.Header().Add("Trailer", key)
	}
	w.WriteHeader(resp.StatusCode)

	if _, err := copyAndFlush(w, resp.Body); err != nil {
		log.Printf("NEAR: HTTP proxy response from %s ended early: %v", outReq.URL.Redacted(), err)
	}
	for key, values := range resp.Trailer {
		w.Header()[key] = values
	}
}

func outboundProxyRequest(r *http.Request) (*http.Request, string, error) {
	if r.URL == nil {
		return nil, "", fmt.Errorf("request has no target URL")
	}
	outReq := r.Clone(r.Context())
	urlCopy := *r.URL
	outReq.URL = &urlCopy
	outReq.Header = r.Header.Clone()
	outReq.RequestURI = ""

	if outReq.URL.Scheme == "" {
		outReq.URL.Scheme = "http"
	}
	outReq.URL.Scheme = strings.ToLower(outReq.URL.Scheme)
	if outReq.URL.Scheme != "http" && outReq.URL.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported proxy URL scheme %q", outReq.URL.Scheme)
	}
	if outReq.URL.Host == "" {
		outReq.URL.Host = r.Host
	}
	if outReq.URL.Host == "" {
		return nil, "", fmt.Errorf("request has no target host")
	}

	// For an absolute-form proxy request, the URI authority is authoritative.
	// Do not let a conflicting Host header select a different virtual host.
	outReq.Host = outReq.URL.Host
	requestUpgrade := preserveUpgradeAndTrailers(outReq.Header)
	outReq.Header.Del("Proxy-Authorization")
	appendVia(outReq.Header, r.ProtoMajor, r.ProtoMinor)
	return outReq, requestUpgrade, nil
}

func (p *salmonHTTPProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	authority := r.Host
	if authority == "" && r.URL != nil {
		authority = r.URL.Host
		if authority == "" {
			authority = r.URL.Path
		}
	}
	target, err := normalizeProxyAuthority(authority, "443")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	upstream, err := p.dialContext(r.Context(), "tcp", target)
	if err != nil {
		log.Printf("NEAR: HTTP CONNECT to %s failed: %v", target, err)
		http.Error(w, "upstream connection failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "CONNECT requires HTTP/1.x connection hijacking", http.StatusHTTPVersionNotSupported)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	_ = client.SetDeadline(time.Time{})
	_ = upstream.SetDeadline(time.Time{})

	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\nProxy-Agent: salmon-cannon\r\n\r\n"); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		client.Close()
		upstream.Close()
		return
	}

	bufferedClient := &proxyBufferedConn{Conn: client, reader: buffered.Reader}
	relayConnData(bufferedClient, upstream)
}

func (p *salmonHTTPProxy) handleUpgrade(w http.ResponseWriter, resp *http.Response, requestUpgrade string) {
	responseUpgrade := headerUpgradeType(resp.Header)
	if requestUpgrade == "" || responseUpgrade == "" || !strings.EqualFold(requestUpgrade, responseUpgrade) {
		http.Error(w, "upstream returned an invalid protocol upgrade", http.StatusBadGateway)
		return
	}
	upstream, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		http.Error(w, "upstream upgrade is not bidirectional", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "protocol upgrades require HTTP/1.x connection hijacking", http.StatusHTTPVersionNotSupported)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})

	removeHopByHopHeaders(resp.Header)
	resp.Header.Set("Connection", "Upgrade")
	resp.Header.Set("Upgrade", responseUpgrade)
	appendVia(resp.Header, resp.ProtoMajor, resp.ProtoMinor)
	resp.Body = nil
	if err := resp.Write(buffered); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	if err := buffered.Flush(); err != nil {
		client.Close()
		upstream.Close()
		return
	}

	relayUpgrade(client, buffered.Reader, upstream)
}

func normalizeProxyAuthority(authority, defaultPort string) (string, error) {
	authority = strings.TrimSpace(authority)
	if authority == "" {
		return "", fmt.Errorf("CONNECT request has no target")
	}
	if strings.ContainsAny(authority, "/?#@ \t\r\n") {
		return "", fmt.Errorf("invalid CONNECT target %q", authority)
	}

	host, portText, err := net.SplitHostPort(authority)
	if err != nil {
		if strings.Count(authority, ":") > 1 && !(strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]")) {
			return "", fmt.Errorf("IPv6 CONNECT targets must use brackets")
		}
		host = strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")
		portText = defaultPort
	}
	if host == "" {
		return "", fmt.Errorf("CONNECT request has no target host")
	}
	port, err := net.LookupPort("tcp", portText)
	if err != nil || port < 1 || port > 65535 {
		return "", fmt.Errorf("invalid CONNECT target port %q", portText)
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

var hopByHopHeaders = []string{
	"Connection",
	"Proxy-Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailer",
	"Transfer-Encoding",
	"Upgrade",
}

func preserveUpgradeAndTrailers(header http.Header) string {
	upgrade := headerUpgradeType(header)
	trailers := headerHasToken(header, "Te", "trailers")
	removeHopByHopHeaders(header)
	if trailers {
		header.Set("Te", "trailers")
	}
	if upgrade != "" {
		header.Set("Connection", "Upgrade")
		header.Set("Upgrade", upgrade)
	}
	return upgrade
}

func removeHopByHopHeaders(header http.Header) {
	for _, connectionValue := range header.Values("Connection") {
		for _, token := range strings.Split(connectionValue, ",") {
			if token = strings.TrimSpace(token); token != "" {
				header.Del(token)
			}
		}
	}
	for _, name := range hopByHopHeaders {
		header.Del(name)
	}
}

func headerUpgradeType(header http.Header) string {
	if !headerHasToken(header, "Connection", "upgrade") {
		return ""
	}
	return strings.TrimSpace(header.Get("Upgrade"))
}

func headerHasToken(header http.Header, name, token string) bool {
	for _, value := range header.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func appendVia(header http.Header, major, minor int) {
	value := fmt.Sprintf("%d.%d salmon-cannon", major, minor)
	if current := header.Get("Via"); current != "" {
		header.Set("Via", current+", "+value)
		return
	}
	header.Set("Via", value)
}

func copyHTTPHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

type flushWriter struct {
	writer  io.Writer
	flusher http.Flusher
}

func (w flushWriter) Write(data []byte) (int, error) {
	n, err := w.writer.Write(data)
	w.flusher.Flush()
	return n, err
}

func copyAndFlush(dst http.ResponseWriter, src io.Reader) (int64, error) {
	if flusher, ok := dst.(http.Flusher); ok {
		return io.Copy(flushWriter{writer: dst, flusher: flusher}, src)
	}
	return io.Copy(dst, src)
}

type proxyBufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *proxyBufferedConn) Read(data []byte) (int, error) {
	return c.reader.Read(data)
}

func (c *proxyBufferedConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return nil
}

func relayUpgrade(client net.Conn, clientReader io.Reader, upstream io.ReadWriteCloser) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(upstream, clientReader)
		_ = upstream.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
	}()

	wg.Wait()
	_ = client.Close()
	_ = upstream.Close()
}
