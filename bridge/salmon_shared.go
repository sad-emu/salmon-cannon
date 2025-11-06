package bridge

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"salmoncannon/crypt"
	"salmoncannon/limiter"
	"sync"

	quic "github.com/quic-go/quic-go"
)

const STATUS_HEADER = 0x01
const CONNECT_HEADER = 0x02
const STATUS_ACK = 0x03
const CONNECT_ENC_HEADER = 0x04

// =========================================================
// Helpers
// =========================================================

// Simple 2-byte length-prefixed ASCII header carrying "host:port".
func WriteTargetHeader(w io.Writer, addr string, sharedSecret string) error {
	if len(addr) > 65535 {
		return fmt.Errorf("target address too long")
	}
	var hdr [3]byte
	hdr[0] = CONNECT_HEADER
	addrToWrite := []byte(addr)
	if sharedSecret != "" {
		hdr[0] = CONNECT_ENC_HEADER
		addrToWriteEnc, err := crypt.EncryptBytesWithSecret([]byte(addr), sharedSecret)
		if err != nil {
			return fmt.Errorf("failed to encrypt target header: %v", err)
		}
		addrToWrite = addrToWriteEnc
	}
	// Don't bother encrypting the header length
	binary.BigEndian.PutUint16(hdr[1:], uint16(len(addrToWrite)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(addrToWrite)
	return err
}

func ReadHeaderType(r io.Reader) (byte, error) {
	var hdrType [1]byte
	if _, err := io.ReadFull(r, hdrType[:]); err != nil {
		return 0, err
	}
	return hdrType[0], nil
}

func ReadTargetHeader(r io.Reader, sharedSecret string) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > 65535 {
		return "", fmt.Errorf("empty target")
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}

	if sharedSecret != "" {
		decBuf, err := crypt.DecryptBytesWithSecret(buf, sharedSecret)
		if err != nil {
			return "", fmt.Errorf("failed to decrypt target header: %v", err)
		}
		buf = decBuf
	}

	return string(buf), nil
}

// bidiPipe moves bytes both ways until EOF on both directions.
// Semantics:
// - When client->stream copy finishes, we FIN the stream write side (stream.Close()).
// - When stream->client copy finishes, we close the TCP socket.
// - On errors, we best-effort cancel the other direction to unblock.
func BidiPipe(stream *quic.Stream, tcp net.Conn,
	limiter *limiter.SharedLimiter, sharedSecret string) {
	var wg sync.WaitGroup
	wg.Add(2)

	// Copy tcp -> stream
	go func() {
		defer wg.Done()

		if sharedSecret != "" {
			tcp = crypt.AesWrapConn(tcp, sharedSecret)
		}

		var src io.Reader
		if limiter != nil {
			src = limiter.WrapConn(tcp)
		} else {
			src = io.Reader(tcp)
		}

		if _, err := io.Copy(stream, src); err != nil {
			stream.CancelWrite(0)
		}
		stream.Close()
		// Force the other direction to stop by setting deadline
		// tcp.SetReadDeadline(time.Now())
	}()

	// Copy stream -> tcp
	go func() {
		defer wg.Done()

		if sharedSecret != "" {
			tcp = crypt.AesWrapConn(tcp, sharedSecret)
		}

		var dst io.Writer
		if limiter != nil {
			dst = limiter.WrapConn(tcp)
		} else {
			dst = io.Writer(tcp)
		}

		if _, err := io.Copy(dst, stream); err != nil {
			stream.CancelRead(0)
		}
		tcp.Close()
		// Force the other direction to stop by canceling stream read
		stream.CancelRead(0)
	}()

	wg.Wait()
}
