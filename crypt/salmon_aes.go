package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"net"
	"salmoncannon/utils"
	"time"
)

type aesCtrConn struct {
	Conn         net.Conn
	initialised  bool
	sharedSecret string
	key          []byte
	iv           []byte
	ctrCipher    cipher.Stream
	encBuf       []byte
	pos          int32
}

const keyRandomHashSizeBytes = 32
const aesKeySizeBytes = 32
const updateKeyAfterBytes = 1024 * 1024 * 100

func EncryptBytesWithSecret(plainText []byte, sharedSecret string) ([]byte, error) {
	plaintextIv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(plaintextIv); err != nil {
		return nil, err
	}
	plaintextKey := make([]byte, aesKeySizeBytes)
	if _, err := rand.Read(plaintextKey); err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(plaintextKey)
	if err != nil {
		return nil, err
	}
	ctrCipher := cipher.NewCTR(block, plaintextIv)

	encBuf := make([]byte, len(plainText))
	ctrCipher.XORKeyStream(encBuf, plainText)

	keyMod := make([]byte, keyRandomHashSizeBytes)
	if _, err := rand.Read(keyMod); err != nil {
		return nil, err
	}
	key, err := utils.DeriveEncKeyFromBytesAndSalt(sharedSecret, keyMod)
	if err != nil {
		return nil, err
	}

	block, err = aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	// Generate iv using system random
	iv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	ctrCipher = cipher.NewCTR(block, iv)

	keyBuff := make([]byte, len(plaintextKey))
	ctrCipher.XORKeyStream(keyBuff, plaintextKey)

	// Prepend keyMod and iv to the ciphertext
	result := make([]byte, keyRandomHashSizeBytes+len(iv)+len(encBuf)+len(plaintextIv)+len(keyBuff))

	copy(result[0:keyRandomHashSizeBytes], keyMod)
	copy(result[keyRandomHashSizeBytes:keyRandomHashSizeBytes+len(iv)], iv)
	copy(result[keyRandomHashSizeBytes+len(iv):keyRandomHashSizeBytes+len(iv)+len(plaintextIv)], plaintextIv)
	copy(result[keyRandomHashSizeBytes+len(iv)+len(plaintextIv):keyRandomHashSizeBytes+len(iv)+len(plaintextIv)+len(keyBuff)], keyBuff)
	copy(result[keyRandomHashSizeBytes+len(iv)+len(plaintextIv)+len(keyBuff):], encBuf)

	return result, nil
}

func DecryptBytesWithSecret(cipherText []byte, sharedSecret string) ([]byte, error) {
	if len(cipherText) < keyRandomHashSizeBytes+aes.BlockSize {
		return nil, errors.New("ciphertext too short")
	}
	keyMod := cipherText[0:keyRandomHashSizeBytes]
	iv := cipherText[keyRandomHashSizeBytes : keyRandomHashSizeBytes+aes.BlockSize]
	plaintextIv := cipherText[keyRandomHashSizeBytes+aes.BlockSize : keyRandomHashSizeBytes+aes.BlockSize+aes.BlockSize]
	keyBuf := cipherText[keyRandomHashSizeBytes+aes.BlockSize+aes.BlockSize : keyRandomHashSizeBytes+aes.BlockSize+aes.BlockSize+aesKeySizeBytes]
	encBuf := cipherText[keyRandomHashSizeBytes+aes.BlockSize+aes.BlockSize+aesKeySizeBytes:]

	key, err := utils.DeriveEncKeyFromBytesAndSalt(sharedSecret, keyMod)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	ctrCipher := cipher.NewCTR(block, iv)

	decryptedKey := make([]byte, len(keyBuf))
	ctrCipher.XORKeyStream(decryptedKey, keyBuf)

	block, err = aes.NewCipher(decryptedKey)
	if err != nil {
		return nil, err
	}
	ctrCipher = cipher.NewCTR(block, plaintextIv)

	plaintext := make([]byte, len(encBuf))
	ctrCipher.XORKeyStream(plaintext, encBuf)

	return plaintext, nil
}

func (t *aesCtrConn) Read(p []byte) (int, error) {
	// Initialise CTR cipher on first read
	if !t.initialised {
		keyMod := make([]byte, keyRandomHashSizeBytes)
		// Read the key modifier from the connection
		n, err := t.Conn.Read(keyMod)
		if err != nil || n != keyRandomHashSizeBytes {
			if err == nil {
				err = errors.New("short read: expected key modifier")
			}
			return 0, err
		}

		var encAesKey []byte
		encAesKey, err = utils.DeriveEncKeyFromBytesAndSalt(t.sharedSecret, keyMod)
		if err != nil {
			return 0, err
		}

		keyIv := make([]byte, aes.BlockSize)
		// Read the AES key IV from the connection
		n, err = t.Conn.Read(keyIv)
		if err != nil || n != aes.BlockSize {
			if err == nil {
				err = errors.New("short read: expected key iv")
			}
			return 0, err
		}

		encKey := make([]byte, aesKeySizeBytes)
		// Read the encrypted AES key from the connection
		n, err = t.Conn.Read(encKey)
		if err != nil || n != aesKeySizeBytes {
			if err == nil {
				err = errors.New("short read: expected encrypted aes key")
			}
			return 0, err
		}

		block, err := aes.NewCipher(encAesKey)
		if err != nil {
			return 0, err
		}
		keyCipher := cipher.NewCTR(block, keyIv)
		t.key = make([]byte, aesKeySizeBytes)
		keyCipher.XORKeyStream(t.key, encKey)

		// Expect the next bytes to be the IV
		iv := make([]byte, aes.BlockSize)
		n, err = t.Conn.Read(iv)
		if err != nil {
			return 0, err
		}
		if n != aes.BlockSize {
			return 0, err
		}

		block, err = aes.NewCipher(t.key)
		t.ctrCipher = cipher.NewCTR(block, iv)
		t.encBuf = make([]byte, len(p))
		t.initialised = true
	}

	// Resize enc buffer if needed
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

func (t *aesCtrConn) initAsWriter() error {
	aesKeyIv := make([]byte, aes.BlockSize)
	if _, err := rand.Read(aesKeyIv); err != nil {
		return err
	}
	t.key = make([]byte, aesKeySizeBytes)
	if _, err := rand.Read(t.key); err != nil {
		return err
	}

	keyMod := make([]byte, keyRandomHashSizeBytes)
	if _, err := rand.Read(keyMod); err != nil {
		return err
	}
	n, err := t.Conn.Write(keyMod)
	if err != nil {
		return err
	}
	var aesKey []byte
	aesKey, err = utils.DeriveEncKeyFromBytesAndSalt(t.sharedSecret, keyMod)
	if err != nil {
		return err
	}

	block, err := aes.NewCipher(aesKey)
	if err != nil {
		return err
	}

	keyCipher := cipher.NewCTR(block, aesKeyIv)
	encKey := make([]byte, len(t.key))
	keyCipher.XORKeyStream(encKey, t.key)

	n, err = t.Conn.Write(aesKeyIv)
	if err != nil {
		return err
	}
	if n != len(aesKeyIv) {
		return err
	}

	n, err = t.Conn.Write(encKey)
	if err != nil {
		return err
	}
	if n != len(encKey) {
		return err
	}

	block, err = aes.NewCipher(t.key)
	if err != nil {
		return err
	}

	t.iv = make([]byte, aes.BlockSize)
	if _, err := rand.Read(t.iv); err != nil {
		return err
	}
	t.ctrCipher = cipher.NewCTR(block, t.iv)

	n, err = t.Conn.Write(t.iv)
	if err != nil {
		return err
	}
	if n != len(t.iv) {
		return err
	}
	return nil
}

func (t *aesCtrConn) Write(p []byte) (int, error) {
	// Initialise CTR cipher on first write
	if !t.initialised {
		if err := t.initAsWriter(); err != nil {
			return 0, err
		}
		t.encBuf = make([]byte, len(p))
		t.initialised = true
	}
	// Encrypt and write data
	if t.encBuf == nil || len(t.encBuf) < len(p) {
		t.encBuf = make([]byte, len(p))
	}

	t.ctrCipher.XORKeyStream(t.encBuf[:len(p)], p)

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

func (t *aesCtrConn) SetDeadline(tm time.Time) error {
	return t.Conn.SetDeadline(tm)
}

func (t *aesCtrConn) SetReadDeadline(tm time.Time) error {
	return t.Conn.SetReadDeadline(tm)
}

func (t *aesCtrConn) SetWriteDeadline(tm time.Time) error {
	return t.Conn.SetWriteDeadline(tm)
}

// WrapConn wraps a net.Conn so all reads/writes are encrypted/decrypted
func AesWrapConn(c net.Conn, sharedSecret string) *aesCtrConn {
	return &aesCtrConn{Conn: c, initialised: false, sharedSecret: sharedSecret}
}
