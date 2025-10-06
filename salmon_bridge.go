package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
)

type SalmonBridge struct {
	BridgePort    int
	BridgeAddress string

	mu      sync.Mutex
	qconn   *quic.Conn // single long-lived QUIC connection
	closing bool
}

// =========================================================
// Near side: dial far, open a new QUIC stream per TCP conn
// =========================================================

func (s *SalmonBridge) ensureQUIC(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return fmt.Errorf("bridge is closing")
	}
	if s.qconn != nil {
		return nil
	}
	addr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)

	tlsConf := &tls.Config{
		InsecureSkipVerify: true, // for prototype
		NextProtos:         []string{"salmon-bridge"},
	}
	qcfg := &quic.Config{
		// Tune as needed:
		MaxIdleTimeout:                 10 * time.Second,
		InitialStreamReceiveWindow:     1024 * 1024 * 10, // 10 MiB
		MaxStreamReceiveWindow:         1024 * 1024 * 40,
		InitialConnectionReceiveWindow: 1024 * 1024 * 40,
		// EnableDatagrams:              false,
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	qc, err := quic.DialAddr(dialCtx, addr, tlsConf, qcfg)
	if err != nil {
		return fmt.Errorf("dial QUIC %s: %w", addr, err)
	}
	s.qconn = qc
	return nil
}

// NewNearConn returns a net.Conn to the caller. Internally, it opens a new QUIC
// stream, sends a small header identifying the remote target (host:port),
// and then pipes bytes bidirectionally.
func (s *SalmonBridge) NewNearConn(host string, port int) (net.Conn, error) {
	if err := s.ensureQUIC(context.Background()); err != nil {
		return nil, err
	}

	// Create a local pipe: return one end to the caller, and connect the other to the QUIC stream.
	clientSide, internal := net.Pipe()

	go func() {
		// Ensure we close the internal end if anything fails here.
		defer internal.Close()

		// Open a fresh bidirectional QUIC stream for this logical connection.
		stream, err := s.qconn.OpenStreamSync(context.Background())
		if err != nil {
			log.Printf("NEAR: OpenStreamSync error: %v", err)
			return
		}
		// Make sure the write side of the stream is FINed after sending client->far bytes.
		defer stream.Close()

		// 1) Send a small header carrying target address.
		target := fmt.Sprintf("%s:%d", host, port)
		if err := writeTargetHeader(stream, target); err != nil {
			log.Printf("NEAR: write header error: %v", err)
			// If we fail before copying, cancel read to unblock far side quickly.
			stream.CancelRead(0)
			return
		}

		// 2) Pump data both ways.
		bidiPipe(stream, internal)
	}()

	return clientSide, nil
}

// =========================================================
// Far side: accept streams, read header, dial target, pipe
// =========================================================

func (s *SalmonBridge) NewFarListen(listenAddr string) error {
	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{generateSelfSignedCert()},
		NextProtos:   []string{"salmon-bridge"},
	}
	qcfg := &quic.Config{
		// Tune as needed (see near side).
	}

	l, err := quic.ListenAddr(listenAddr, tlsConf, qcfg)
	if err != nil {
		return fmt.Errorf("listen QUIC %s: %w", listenAddr, err)
	}
	log.Printf("FAR listening on %s", listenAddr)

	for {
		qc, err := l.Accept(context.Background())
		if err != nil {
			log.Printf("FAR: Accept conn error: %v", err)
			continue
		}

		go func(conn *quic.Conn) {
			for {
				stream, err := conn.AcceptStream(context.Background())
				if err != nil {
					log.Printf("FAR: AcceptStream error: %v", err)
					return
				}
				go s.handleIncomingStream(stream)
			}
		}(qc)
	}
}

func (s *SalmonBridge) handleIncomingStream(stream *quic.Stream) {
	// 1) Read target header.
	target, err := readTargetHeader(stream)
	if err != nil {
		log.Printf("FAR: read header error: %v")
		stream.CancelRead(0)
		stream.Close()
		return
	}

	// 2) Dial target TCP.
	dst, err := net.Dial("tcp", target)
	if err != nil {
		log.Printf("FAR: dial %s error: %v", target, err)
		stream.CancelRead(0)
		stream.Close()
		return
	}
	// Ensure we close both sides.
	defer dst.Close()
	defer stream.Close()

	// 3) Pipe bytes both directions.
	bidiPipe(stream, dst)
}

// =========================================================
// Helpers
// =========================================================

// Simple 2-byte length-prefixed ASCII header carrying "host:port".
func writeTargetHeader(w io.Writer, addr string) error {
	if len(addr) > 65535 {
		return fmt.Errorf("target address too long")
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(addr)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte(addr))
	return err
}

func readTargetHeader(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 {
		return "", fmt.Errorf("empty target")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// bidiPipe moves bytes both ways until EOF on both directions.
// Semantics:
// - When client->stream copy finishes, we FIN the stream write side (stream.Close()).
// - When stream->client copy finishes, we close the TCP socket.
// - On errors, we best-effort cancel the other direction to unblock.
func bidiPipe(stream *quic.Stream, tcp net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	// client -> stream
	go func() {
		defer wg.Done()
		if _, err := io.Copy(stream, tcp); err != nil {
			// Abort sending on the stream if write fails.
			stream.CancelWrite(0)
		}
		// Signal FIN on stream write side.
		_ = stream.Close()
	}()

	// stream -> client
	go func() {
		defer wg.Done()
		if _, err := io.Copy(tcp, stream); err != nil {
			// Abort reading from stream if copy fails.
			stream.CancelRead(0)
		}
		_ = tcp.Close()
	}()

	wg.Wait()
}
