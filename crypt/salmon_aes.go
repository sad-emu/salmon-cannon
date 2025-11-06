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
	Conn             net.Conn
	readInitialised  bool
	writeInitialised bool
	sharedSecret     string
	ctrReadCipher    cipher.Stream
	ctrWriteCipher   cipher.Stream
	encReadBuf       []byte
	encWriteBuf      []byte
	iv               []byte
	key              []byte
}

const keyRandomHashSizeBytes = 32
const aesKeySizeBytes = 32

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
	// Resize enc buffer if needed
	if t.encReadBuf == nil || len(t.encReadBuf) < len(p) {
		t.encReadBuf = make([]byte, len(p))
	}

	n, err := t.Conn.Read(t.encReadBuf)
	if err != nil {
		return n, err
	}

	// Decrypt data
	t.ctrReadCipher.XORKeyStream(p[:n], t.encReadBuf[:n])

	return n, nil
}

func (t *aesCtrConn) Write(p []byte) (int, error) {
	// Encrypt and write data
	if t.encWriteBuf == nil || len(t.encWriteBuf) < len(p) {
		t.encWriteBuf = make([]byte, len(p))
	}

	t.ctrWriteCipher.XORKeyStream(t.encWriteBuf[:len(p)], p)

	return t.Conn.Write(t.encWriteBuf[:len(p)])
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
func AesWrapConn(c net.Conn, readIv []byte, readKey []byte, writeIv []byte, writeKey []byte) *aesCtrConn {
	readBlock, err := aes.NewCipher(readKey)
	if err != nil {
		return nil
	}
	ctrReadCipher := cipher.NewCTR(readBlock, readIv)
	writeBlock, err := aes.NewCipher(writeKey)
	if err != nil {
		return nil
	}
	ctrWriteCipher := cipher.NewCTR(writeBlock, writeIv)
	return &aesCtrConn{Conn: c, writeInitialised: false, readInitialised: false, ctrReadCipher: ctrReadCipher, ctrWriteCipher: ctrWriteCipher}
}
