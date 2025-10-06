package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"

	quic "github.com/quic-go/quic-go"
)

// =========================================================
// Helpers
// =========================================================

// Simple 2-byte length-prefixed ASCII header carrying "host:port".
func WriteTargetHeader(w io.Writer, addr string) error {
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

func ReadTargetHeader(r io.Reader) (string, error) {
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
func BidiPipe(stream *quic.Stream, tcp net.Conn, limiter *SharedLimiter) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		src := tcp
		if limiter != nil {
			src = limiter.WrapConn(tcp)
		}
		if _, err := io.Copy(stream, src); err != nil {
			stream.CancelWrite(0)
		}
		_ = stream.Close()
	}()

	go func() {
		defer wg.Done()
		dst := tcp
		if limiter != nil {
			dst = limiter.WrapConn(tcp)
		}
		if _, err := io.Copy(dst, stream); err != nil {
			stream.CancelRead(0)
		}
		_ = tcp.Close()
	}()

	wg.Wait()
}
