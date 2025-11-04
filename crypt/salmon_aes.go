package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"net"
	"time"
)

type aesCtrConn struct {
	Conn        net.Conn
	initialised bool
	key         []byte
	iv          []byte
	ctrCipher   cipher.Stream
	encBuf      []byte
}

func (t *aesCtrConn) Read(p []byte) (int, error) {
	// Initialise CTR cipher on first read
	if !t.initialised {
		block, err := aes.NewCipher(t.key)
		if err != nil {
			return 0, err
		}
		// Expect the first bytes to be the IV
		iv := make([]byte, aes.BlockSize)
		n, err := t.Conn.Read(iv)
		if err != nil {
			return 0, err
		}
		if n != aes.BlockSize {
			return 0, err
		}
		t.ctrCipher = cipher.NewCTR(block, iv)
		t.encBuf = make([]byte, len(p))
		t.initialised = true
	}

	// Read encrypted data
	if t.encBuf == nil || len(t.encBuf) < len(p) {
		t.encBuf = make([]byte, len(p))
	}

	n, err := t.Conn.Read(t.encBuf)
	if err != nil {
		return n, err
	}

	// Decrypt data
	t.ctrCipher.XORKeyStream(p[:n], t.encBuf[:n])
	return n, nil
}

func (t *aesCtrConn) Write(p []byte) (int, error) {
	// Initialise CTR cipher on first write
	if !t.initialised {
		block, err := aes.NewCipher(t.key)
		if err != nil {
			return 0, err
		}
		// Generate iv using system random
		t.iv = make([]byte, aes.BlockSize)
		if _, err := rand.Read(t.iv); err != nil {
			return 0, err
		}
		t.ctrCipher = cipher.NewCTR(block, t.iv)
		// try send the iv as the first bytes
		n, err := t.Conn.Write(t.iv)
		if err != nil {
			return 0, err
		}
		if n != len(t.iv) {
			return 0, err
		}
		t.encBuf = make([]byte, len(p))
		t.initialised = true
	}
	// Encrypt and write data
	if t.encBuf == nil || len(t.encBuf) < len(p) {
		t.encBuf = make([]byte, len(p))
	}
	t.ctrCipher.XORKeyStream(t.encBuf, p)
	return t.Conn.Write(t.encBuf[:len(p)])
}

func (t *aesCtrConn) Close() error {
	return t.Conn.Close()
}

func (t *aesCtrConn) LocalAddr() net.Addr {
	return t.Conn.LocalAddr()
}

func (t *aesCtrConn) RemoteAddr() net.Addr {
	return t.Conn.RemoteAddr()
}

func (t *aesCtrConn) SetDeadline(deadline time.Time) error {
	return t.Conn.SetDeadline(deadline)
}

func (t *aesCtrConn) SetReadDeadline(deadline time.Time) error {
	return t.Conn.SetReadDeadline(deadline)
}

func (t *aesCtrConn) SetWriteDeadline(deadline time.Time) error {
	return t.Conn.SetWriteDeadline(deadline)
}

// WrapConn wraps a net.Conn so all reads/writes are encrypted/decrypted
func AesWrapConn(c net.Conn, key []byte) net.Conn {
	return &aesCtrConn{Conn: c, initialised: false, key: key}
}
