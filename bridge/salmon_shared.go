package bridge

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"salmoncannon/crypt"
	"salmoncannon/limiter"
	"sync"
	"time"

	quic "github.com/quic-go/quic-go"
)

const STATUS_HEADER = 0x01
const CONNECT_HEADER = 0x02
const STATUS_ACK = 0x03
const CONNECT_ENC_HEADER = 0x04

const CONNECT_ENC_PAYLOAD_SIZE = 192

// Simple 2-byte length-prefixed ASCII header carrying "host:port".
func WriteTargetHeader(w io.Writer, addr string) error {
	if len(addr) > 65535 {
		return fmt.Errorf("target address too long")
	}
	var hdr [3]byte
	hdr[0] = CONNECT_HEADER
	addrToWrite := []byte(addr)
	// Don't bother encrypting the header length
	binary.BigEndian.PutUint16(hdr[1:], uint16(len(addrToWrite)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(addrToWrite)
	return err
}

func WriteTargetHeaderEnc(w io.Writer, addr string, readIv []byte, writeIv []byte, readKey []byte, writeKey []byte, sharedSecret string) error {
	if len(addr) > 65535 {
		return fmt.Errorf("target address too long")
	}
	var hdr [3]byte
	hdr[0] = CONNECT_ENC_HEADER
	addrToWriteEnc, err := crypt.EncryptBytesWithSecret([]byte(addr), sharedSecret)
	if err != nil {
		return fmt.Errorf("failed to encrypt target header: %v", err)
	}
	binary.BigEndian.PutUint16(hdr[1:], uint16(len(addrToWriteEnc)))
	// readIv, writeIv, readKey, writeKey into header
	ivKeyBuf := make([]byte, 16+32+16+32)
	copy(ivKeyBuf[0:16], readIv)
	copy(ivKeyBuf[16:32], writeIv)
	copy(ivKeyBuf[32:64], readKey)
	copy(ivKeyBuf[64:96], writeKey)

	encPayload, err := crypt.EncryptBytesWithSecret(ivKeyBuf, sharedSecret)
	if err != nil {
		return fmt.Errorf("failed to encrypt iv/key header: %v", err)
	}

	// Send both as a single payload
	fullPayload := append(hdr[:], addrToWriteEnc...)
	fullPayload = append(fullPayload, encPayload...)
	_, err = w.Write(fullPayload)
	return err
}

func ReadHeaderType(r io.Reader) (byte, error) {
	var hdrType [1]byte
	if _, err := io.ReadFull(r, hdrType[:]); err != nil {
		return 0, err
	}
	return hdrType[0], nil
}

func ReadTargetHeader(r io.Reader) (string, error) {
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
	return string(buf), nil
}

func ReadTargetHeaderEnc(r io.Reader, sharedSecret string) (string, []byte, []byte, []byte, []byte, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", nil, nil, nil, nil, err
	}
	n := int(binary.BigEndian.Uint16(hdr[:]))
	if n == 0 || n > 65535 {
		return "", nil, nil, nil, nil, fmt.Errorf("empty target")
	}
	buf := make([]byte, n+CONNECT_ENC_PAYLOAD_SIZE)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", nil, nil, nil, nil, err
	}

	decBuf, err := crypt.DecryptBytesWithSecret(buf[:n], sharedSecret)
	target := string(decBuf)
	if err != nil {
		return "", nil, nil, nil, nil, fmt.Errorf("failed to decrypt target header: %v", err)
	}

	decBuf, err = crypt.DecryptBytesWithSecret(buf[n:], sharedSecret)
	if err != nil {
		return "", nil, nil, nil, nil, fmt.Errorf("failed to decrypt target header: %v", err)
	}

	if len(decBuf) < 16+32+16+32 {
		return "", nil, nil, nil, nil, fmt.Errorf("decrypted target header too short")
	}

	readIv := decBuf[:16]
	writeIv := decBuf[16:32]
	readKey := decBuf[32:64]
	writeKey := decBuf[64:96]

	return target, readIv, writeIv, readKey, writeKey, nil
}

// bidiPipe moves bytes both ways until EOF on both directions.
// Semantics:
// - When client->stream copy finishes, we FIN the stream write side (stream.Close()).
// - When stream->client copy finishes, we close the TCP socket.
// - On errors, we best-effort cancel the other direction to unblock.
func BidiPipe(stream *quic.Stream, tcp net.Conn,
	limiter *limiter.SharedLimiter, readIv []byte, readKey []byte, writeIv []byte, writeKey []byte) {
	var wg sync.WaitGroup
	wg.Add(2)

	if len(readIv) != 0 && len(readKey) != 0 {
		tcp = crypt.AesWrapConn(tcp, readIv, readKey, writeIv, writeKey)
	}

	// Copy tcp -> stream
	go func() {
		defer wg.Done()

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
		tcp.SetReadDeadline(time.Now())
	}()

	// Copy stream -> tcp
	go func() {
		defer wg.Done()

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
