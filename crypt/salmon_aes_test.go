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

func TestAesWrapQuicStream(t *testing.T) {
	mock := newMockNetConn()
	key := make([]byte, 32)
	rand.Read(key)
	keyStr := base64.StdEncoding.EncodeToString(key)

	wrapped := AesWrapConn(mock, keyStr)
	if wrapped == nil {
		t.Fatal("AesWrapQuicStream returned nil")
	}
	if wrapped.Conn == nil {
		t.Error("Wrapped connection is nil")
	}
	if wrapped.sharedSecret != keyStr {
		t.Error("Shared secret not set correctly")
	}
}

func TestAesEncryptDecrypt(t *testing.T) {
	clientToServer := newMockNetConn()
	serverToClient := newMockNetConn()

	key := make([]byte, 32)
	rand.Read(key)
	keyStr := base64.StdEncoding.EncodeToString(key)

	clientConn := AesWrapConn(clientToServer, keyStr)
	serverConn := AesWrapConn(serverToClient, keyStr)

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

func TestAesEncryptDecryptLarge(t *testing.T) {
	clientToServer := newMockNetConn()
	serverToClient := newMockNetConn()

	key := make([]byte, 32)
	rand.Read(key)
	keyStr := base64.StdEncoding.EncodeToString(key)

	clientConn := AesWrapConn(clientToServer, keyStr)
	serverConn := AesWrapConn(serverToClient, keyStr)

	// 200mb of random data
	testData := make([]byte, 200*1024*1024)
	rand.Read(testData)

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
