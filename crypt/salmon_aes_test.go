package crypt

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"net"
	"testing"
	"time"
)

// mockNetConn implements quic.Stream for testing
type mockNetConn struct {
	readBuf   *bytes.Buffer
	writeBuf  *bytes.Buffer
	closed    bool
	readErr   error
	writeErr  error
	cancelErr error
}

func newMockNetConn() *mockNetConn {
	return &mockNetConn{
		readBuf:  bytes.NewBuffer(nil),
		writeBuf: bytes.NewBuffer(nil),
	}
}

func (t *mockNetConn) Read(p []byte) (int, error) {
	if t.readErr != nil {
		return 0, t.readErr
	}
	return t.readBuf.Read(p)
}

func (t *mockNetConn) Write(p []byte) (int, error) {
	if t.writeErr != nil {
		return 0, t.writeErr
	}
	return t.writeBuf.Write(p)
}

func (t *mockNetConn) Close() error {
	return nil
}

func (t *mockNetConn) LocalAddr() net.Addr {
	return nil
}

func (t *mockNetConn) RemoteAddr() net.Addr {
	return nil
}

func (t *mockNetConn) SetDeadline(tm time.Time) error {
	return nil
}

func (t *mockNetConn) SetReadDeadline(tm time.Time) error {
	return nil
}

func (t *mockNetConn) SetWriteDeadline(tm time.Time) error {
	return nil
}

func TestEncryptBytesWithSecret(t *testing.T) {
	plainText := []byte("This is a test message for encryption.")
	key := make([]byte, 32)
	rand.Read(key)
	sharedSecret := base64.StdEncoding.EncodeToString(key)

	encData, err := EncryptBytesWithSecret(plainText, sharedSecret)
	if err != nil {
		t.Fatalf("EncryptBytesWithSecret failed: %v", err)
	}

	decData, err := DecryptBytesWithSecret(encData, sharedSecret)
	if err != nil {
		t.Fatalf("DecryptBytesWithSecret failed: %v", err)
	}

	if !bytes.Equal(decData, plainText) {
		t.Fatalf("Decrypted data does not match original.\nExpected: %s\nGot: %s", plainText, decData)
	}
}

func TestAesWrapQuicStream(t *testing.T) {
	mock := newMockNetConn()
	readIv := make([]byte, 16)
	writeIv := make([]byte, 16)
	readKey := make([]byte, 32)
	writeKey := make([]byte, 32)
	rand.Read(readIv)
	rand.Read(writeIv)
	rand.Read(readKey)
	rand.Read(writeKey)

	wrapped := AesWrapConn(mock, readIv, readKey, writeIv, writeKey)
	if wrapped == nil {
		t.Fatal("AesWrapQuicStream returned nil")
	}
	if wrapped.Conn == nil {
		t.Error("Wrapped connection is nil")
	}
}

func TestAesEncryptDecrypt(t *testing.T) {
	clientToServer := newMockNetConn()
	serverToClient := newMockNetConn()

	readIv := make([]byte, 16)
	readKey := make([]byte, 32)
	rand.Read(readIv)
	rand.Read(readKey)

	clientConn := AesWrapConn(clientToServer, readIv, readKey, readIv, readKey)
	serverConn := AesWrapConn(serverToClient, readIv, readKey, readIv, readKey)

	testData := []byte("Hello, World! This is a test message.")

	n, err := clientConn.Write(testData)
	if err != nil {
		t.Fatalf("Client write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Client write: expected %d bytes, got %d", len(testData), n)
	}

	serverToClient.readBuf = bytes.NewBuffer(clientToServer.writeBuf.Bytes())

	readBuf := make([]byte, len(testData))
	n, err = serverConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Server read: expected %d bytes, got %d", len(testData), n)
	}

	if !bytes.Equal(readBuf[:n], testData) {
		t.Fatalf("Decrypted data doesn't match original.\nExpected: %s\nGot: %s", testData, readBuf[:n])
	}
}

func TestAesEncryptDecryptBiDi(t *testing.T) {
	clientToServer := newMockNetConn()
	serverToClient := newMockNetConn()

	readIv := make([]byte, 16)
	writeIv := make([]byte, 16)
	readKey := make([]byte, 32)
	writeKey := make([]byte, 32)
	rand.Read(readIv)
	rand.Read(writeIv)
	rand.Read(readKey)
	rand.Read(writeKey)

	clientConn := AesWrapConn(clientToServer, readIv, readKey, writeIv, writeKey)
	serverConn := AesWrapConn(serverToClient, writeIv, writeKey, readIv, readKey)

	testData := []byte("Hello, World! This is a test message.")

	n, err := clientConn.Write(testData)
	if err != nil {
		t.Fatalf("Client write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Client write: expected %d bytes, got %d", len(testData), n)
	}

	serverToClient.readBuf = bytes.NewBuffer(clientToServer.writeBuf.Bytes())

	readBuf := make([]byte, len(testData))
	n, err = serverConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Server read: expected %d bytes, got %d", len(testData), n)
	}

	if !bytes.Equal(readBuf[:n], testData) {
		t.Fatalf("Decrypted data doesn't match original.\nExpected: %s\nGot: %s", testData, readBuf[:n])
	}

	n, err = serverConn.Write(testData)
	if err != nil {
		t.Fatalf("Server write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Server write: expected %d bytes, got %d", len(testData), n)
	}

	clientToServer.readBuf = bytes.NewBuffer(serverToClient.writeBuf.Bytes())

	readBuf = make([]byte, len(testData))
	n, err = clientConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Client read failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Client read: expected %d bytes, got %d", len(testData), n)
	}

	if !bytes.Equal(readBuf[:n], testData) {
		t.Fatalf("Decrypted data doesn't match original.\nExpected: %s\nGot: %s", testData, readBuf[:n])
	}
}

func TestAesEncryptDecryptLarge(t *testing.T) {
	clientToServer := newMockNetConn()
	serverToClient := newMockNetConn()

	readIv := make([]byte, 16)
	writeIv := make([]byte, 16)
	readKey := make([]byte, 32)
	writeKey := make([]byte, 32)
	rand.Read(readIv)
	rand.Read(writeIv)
	rand.Read(readKey)
	rand.Read(writeKey)

	clientConn := AesWrapConn(clientToServer, readIv, readKey, writeIv, writeKey)
	serverConn := AesWrapConn(serverToClient, writeIv, writeKey, readIv, readKey)

	// 200mb of random data
	testData := make([]byte, 200*1024*1024)
	rand.Read(testData)

	// Do the write in 10 chunks to avoid overwhelming buffers
	chunkSize := len(testData) / 10
	for i := 0; i < 10; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if i == 9 {
			end = len(testData)
		}
		n, err := clientConn.Write(testData[start:end])
		if err != nil {
			t.Fatalf("Client write chunk %d failed: %v", i, err)
		}
		if n != end-start {
			t.Fatalf("Client write chunk %d: expected %d bytes, got %d", i, end-start, n)
		}
	}

	//n, err := clientConn.Write(testData)
	// if err != nil {
	// 	t.Fatalf("Client write failed: %v", err)
	// }
	// if n != len(testData) {
	// 	t.Fatalf("Client write: expected %d bytes, got %d", len(testData), n)
	// }

	serverToClient.readBuf = bytes.NewBuffer(clientToServer.writeBuf.Bytes())

	readBuf := make([]byte, len(testData))
	n, err := serverConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Server read: expected %d bytes, got %d", len(testData), n)
	}

	if !bytes.Equal(readBuf[:n], testData) {
		t.Fatalf("Decrypted data doesn't match original. Too long to print.")
	}
}
