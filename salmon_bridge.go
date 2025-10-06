package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
)

type SalmonBridge struct {
	BridgePort    int
	BridgeAddress string

	mu    sync.Mutex
	qconn *quic.Conn // single long-lived QUIC connection

	sl *SharedLimiter

	bridgeDown bool
	qcfg       *quic.Config
	tlscfg     *tls.Config
}

// =========================================================
// Near side: dial far, open a new QUIC stream per TCP conn
// =========================================================

func (s *SalmonBridge) ensureQUIC(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.qconn != nil && s.bridgeDown == false {
		return nil
	}

	if s.qconn != nil {
		s.qconn.CloseWithError(0, "reconnecting")
		s.qconn = nil
	}

	addr := fmt.Sprintf("%s:%d", s.BridgeAddress, s.BridgePort)

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	qc, err := quic.DialAddr(dialCtx, addr, s.tlscfg, s.qcfg)
	if err != nil {
		return fmt.Errorf("dial QUIC %s: %w", addr, err)
	}
	s.bridgeDown = false
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
			s.mu.Lock()
			defer s.mu.Unlock()
			s.bridgeDown = true
			log.Printf("NEAR: OpenStreamSync error: %v", err)
			return
		}
		// Make sure the write side of the stream is FINed after sending client->far bytes.
		defer stream.Close()

		// 1) Send a small header carrying target address.
		target := fmt.Sprintf("%s:%d", host, port)
		if err := WriteTargetHeader(stream, target); err != nil {
			log.Printf("NEAR: write header error: %v", err)
			// If we fail before copying, cancel read to unblock far side quickly.
			stream.CancelRead(0)
			return
		}

		// 2) Pump data both ways.
		BidiPipe(stream, internal, s.sl)
	}()

	return clientSide, nil
}

// =========================================================
// Far side: accept streams, read header, dial target, pipe
// =========================================================

func (s *SalmonBridge) NewFarListen(listenAddr string) error {

	l, err := quic.ListenAddr(listenAddr, s.tlscfg, s.qcfg)
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
	target, err := ReadTargetHeader(stream)
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
	BidiPipe(stream, dst, s.sl)
}
