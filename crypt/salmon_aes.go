package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"salmoncannon/utils"

	"github.com/quic-go/quic-go"
)

type aesCtrConn struct {
	Stream       *quic.Stream
	initialised  bool
	sharedSecret string
	key          []byte
	iv           []byte
	ctrCipher    cipher.Stream
	encBuf       []byte
}

const keyRandomHashSizeBytes = 32

func EncryptBytesWithSecret(plainText []byte, sharedSecret string) ([]byte, error) {
	keyMod := make([]byte, keyRandomHashSizeBytes)
	if _, err := rand.Read(keyMod); err != nil {
		return nil, err
	}
	key, err := utils.DeriveEncKeyFromBytesAndSalt(sharedSecret, keyMod)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// Generate iv using system random
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ctrCipher := cipher.NewCTR(block, iv)

	encBuf := make([]byte, len(plainText))
	ctrCipher.XORKeyStream(encBuf, plainText)

	// Prepend keyMod and iv to the ciphertext
	result := make([]byte, keyRandomHashSizeBytes+len(iv)+len(encBuf))
	copy(result[0:keyRandomHashSizeBytes], keyMod)
	copy(result[keyRandomHashSizeBytes:keyRandomHashSizeBytes+len(iv)], iv)
	copy(result[keyRandomHashSizeBytes+len(iv):], encBuf)

	return result, nil
}

func DecryptBytesWithSecret(cipherText []byte, sharedSecret string) ([]byte, error) {
	if len(cipherText) < keyRandomHashSizeBytes+aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}
	keyMod := cipherText[0:keyRandomHashSizeBytes]
	iv := cipherText[keyRandomHashSizeBytes : keyRandomHashSizeBytes+aes.BlockSize]
	encBuf := cipherText[keyRandomHashSizeBytes+aes.BlockSize:]

	key, err := utils.DeriveEncKeyFromBytesAndSalt(sharedSecret, keyMod)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ctrCipher := cipher.NewCTR(block, iv)

	plainText := make([]byte, len(encBuf))
	ctrCipher.XORKeyStream(plainText, encBuf)

	return plainText, nil
}

func (t *aesCtrConn) Read(p []byte) (int, error) {
	// Initialise CTR cipher on first read
	if !t.initialised {
		keyMod := make([]byte, keyRandomHashSizeBytes)
		// Read the key modifier from the connection
		n, err := t.Stream.Read(keyMod)
		if err != nil || n != keyRandomHashSizeBytes {
			if err == nil {
				err = errors.New("short read: expected key modifier")
			}
			return 0, err
		}

		t.key, err = utils.DeriveEncKeyFromBytesAndSalt(t.sharedSecret, keyMod)
		if err != nil {
			return 0, err
		}

		block, err := aes.NewCipher(t.key)
		if err != nil {
			return 0, err
		}
		// Expect the next bytes to be the IV
		iv := make([]byte, aes.BlockSize)
		n, err = t.Stream.Read(iv)
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

	n, err := t.Stream.Read(t.encBuf)
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
		keyMod := make([]byte, keyRandomHashSizeBytes)
		if _, err := rand.Read(keyMod); err != nil {
			return 0, err
		}
		n, err := t.Stream.Write(keyMod)
		if err != nil {
			return 0, err
		}
		t.key, err = utils.DeriveEncKeyFromBytesAndSalt(t.sharedSecret, keyMod)
		if err != nil {
			return 0, err
		}

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
		n, err = t.Stream.Write(t.iv)
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
	return t.Stream.Write(t.encBuf[:len(p)])
}

// WrapConn wraps a net.Conn so all reads/writes are encrypted/decrypted
// func AesWrapConn(c net.Conn, sharedSecret string) net.Conn {
// 	return &aesCtrConn{Conn: c, initialised: false, sharedSecret: sharedSecret}
// }

func AesWrapQuicStream(s *quic.Stream, sharedSecret string) *aesCtrConn {
	return &aesCtrConn{Stream: s, initialised: false, sharedSecret: sharedSecret}
}
