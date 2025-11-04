package crypt

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
)

// mockConn implements net.Conn for testing
type mockConn struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	closed   bool
}

func newMockConn() *mockConn {
	return &mockConn{
		readBuf:  &bytes.Buffer{},
		writeBuf: &bytes.Buffer{},
	}
}

func (m *mockConn) Read(b []byte) (int, error) {
	if m.closed {
		return 0, io.EOF
	}
	return m.readBuf.Read(b)
}

func (m *mockConn) Write(b []byte) (int, error) {
	if m.closed {
		return 0, io.ErrClosedPipe
	}
	return m.writeBuf.Write(b)
}

func (m *mockConn) Close() error {
	m.closed = true
	return nil
}

func (m *mockConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234}
}

func (m *mockConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5678}
}

func (m *mockConn) SetDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetReadDeadline(t time.Time) error {
	return nil
}

func (m *mockConn) SetWriteDeadline(t time.Time) error {
	return nil
}

func TestAesWrapConn(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32) // 256-bit key
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)
	if wrapped == nil {
		t.Fatal("AesWrapConn returned nil")
	}

	// Verify it implements net.Conn
	var _ net.Conn = wrapped
}

func TestAesEncryptDecrypt(t *testing.T) {
	// Create a pair of connected mock connections
	clientToServer := newMockConn()
	serverToClient := newMockConn()

	// Use the same key for both sides
	key := make([]byte, 32)
	rand.Read(key)

	// Wrap both connections
	clientConn := AesWrapConn(clientToServer, key)
	serverConn := AesWrapConn(serverToClient, key)

	// Test data
	testData := []byte("Hello, World! This is a test message.")

	// Client writes encrypted data
	n, err := clientConn.Write(testData)
	if err != nil {
		t.Fatalf("Client write failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Client write: expected %d bytes, got %d", len(testData), n)
	}

	// Transfer encrypted data from client's writeBuf to server's readBuf
	serverToClient.readBuf = bytes.NewBuffer(clientToServer.writeBuf.Bytes())

	// Server reads and decrypts data
	readBuf := make([]byte, len(testData))
	n, err = serverConn.Read(readBuf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if n != len(testData) {
		t.Fatalf("Server read: expected %d bytes, got %d", len(testData), n)
	}

	// Verify decrypted data matches original
	if !bytes.Equal(readBuf[:n], testData) {
		t.Fatalf("Decrypted data doesn't match original.\nExpected: %s\nGot: %s", testData, readBuf[:n])
	}
}

func TestAesBidirectionalEncryption(t *testing.T) {
	// Create mock connections
	conn1 := newMockConn()
	conn2 := newMockConn()

	// Use the same key
	key := make([]byte, 32)
	rand.Read(key)

	// Wrap connections
	wrapped1 := AesWrapConn(conn1, key)
	wrapped2 := AesWrapConn(conn2, key)

	// Test data
	message1 := []byte("Client to Server")
	message2 := []byte("Server to Client")

	// Client -> Server
	wrapped1.Write(message1)
	conn2.readBuf = bytes.NewBuffer(conn1.writeBuf.Bytes())

	buf := make([]byte, len(message1))
	n, err := wrapped2.Read(buf)
	if err != nil {
		t.Fatalf("Server read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], message1) {
		t.Errorf("Server received incorrect data.\nExpected: %s\nGot: %s", message1, buf[:n])
	}

	// Server -> Client (reset buffers)
	conn1.writeBuf.Reset()
	conn2.writeBuf.Reset()

	wrapped2.Write(message2)
	conn1.readBuf = bytes.NewBuffer(conn2.writeBuf.Bytes())

	buf = make([]byte, len(message2))
	n, err = wrapped1.Read(buf)
	if err != nil {
		t.Fatalf("Client read failed: %v", err)
	}
	if !bytes.Equal(buf[:n], message2) {
		t.Errorf("Client received incorrect data.\nExpected: %s\nGot: %s", message2, buf[:n])
	}
}

func TestAesMultipleWrites(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// Write multiple messages
	messages := [][]byte{
		[]byte("First message"),
		[]byte("Second message"),
		[]byte("Third message"),
	}

	for _, msg := range messages {
		n, err := wrapped.Write(msg)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len(msg) {
			t.Fatalf("Expected to write %d bytes, wrote %d", len(msg), n)
		}
	}

	// Verify IV was only written once (on first write)
	// IV is 16 bytes for AES
	totalExpected := 16 // IV
	for _, msg := range messages {
		totalExpected += len(msg)
	}

	if mock.writeBuf.Len() != totalExpected {
		t.Errorf("Expected %d total bytes written, got %d", totalExpected, mock.writeBuf.Len())
	}
}

func TestAesInvalidKeySize(t *testing.T) {
	mock := newMockConn()

	// Test with invalid key sizes
	invalidKeys := [][]byte{
		make([]byte, 0),  // Empty key
		make([]byte, 8),  // Too short
		make([]byte, 15), // Invalid size
		make([]byte, 31), // Invalid size
	}

	for _, key := range invalidKeys {
		wrapped := AesWrapConn(mock, key)

		// Try to write - should fail on cipher initialization
		_, err := wrapped.Write([]byte("test"))
		if err == nil {
			t.Errorf("Expected error with key size %d, but got none", len(key))
		}
	}
}

func TestAesValidKeySizes(t *testing.T) {
	validSizes := []int{16, 24, 32} // AES-128, AES-192, AES-256

	for _, size := range validSizes {
		mock := newMockConn()
		key := make([]byte, size)
		rand.Read(key)

		wrapped := AesWrapConn(mock, key)

		// Should work without error
		_, err := wrapped.Write([]byte("test"))
		if err != nil {
			t.Errorf("Unexpected error with key size %d: %v", size, err)
		}
	}
}

func TestAesReadWithoutWrite(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// Try to read when no data has been written
	buf := make([]byte, 100)
	_, err := wrapped.Read(buf)

	// Should fail because no IV was sent
	if err == nil {
		t.Error("Expected error when reading without IV, got none")
	}
}

func TestAesIVTransmission(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// First write should send IV (16 bytes) + encrypted data
	testData := []byte("test")
	wrapped.Write(testData)

	// Check that IV + encrypted data was written
	written := mock.writeBuf.Bytes()
	if len(written) < 16 {
		t.Fatalf("Expected at least 16 bytes (IV), got %d", len(written))
	}

	// IV should be the first 16 bytes
	iv := written[:16]

	// Verify IV is not all zeros (should be random)
	allZeros := true
	for _, b := range iv {
		if b != 0 {
			allZeros = false
			break
		}
	}
	if allZeros {
		t.Error("IV appears to be all zeros, should be random")
	}
}

func TestAesConnectionMethods(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// Test LocalAddr
	localAddr := wrapped.LocalAddr()
	if localAddr == nil {
		t.Error("LocalAddr returned nil")
	}

	// Test RemoteAddr
	remoteAddr := wrapped.RemoteAddr()
	if remoteAddr == nil {
		t.Error("RemoteAddr returned nil")
	}

	// Test SetDeadline
	if err := wrapped.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetDeadline failed: %v", err)
	}

	// Test SetReadDeadline
	if err := wrapped.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetReadDeadline failed: %v", err)
	}

	// Test SetWriteDeadline
	if err := wrapped.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("SetWriteDeadline failed: %v", err)
	}

	// Test Close
	if err := wrapped.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	if !mock.closed {
		t.Error("Underlying connection was not closed")
	}
}

func TestAesLargeData(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// Create large test data (1MB)
	largeData := make([]byte, 1024*1024)
	rand.Read(largeData)

	// Write large data
	n, err := wrapped.Write(largeData)
	if err != nil {
		t.Fatalf("Failed to write large data: %v", err)
	}
	if n != len(largeData) {
		t.Fatalf("Expected to write %d bytes, wrote %d", len(largeData), n)
	}

	// Verify total bytes written (IV + encrypted data)
	expectedTotal := 16 + len(largeData)
	if mock.writeBuf.Len() != expectedTotal {
		t.Errorf("Expected %d total bytes, got %d", expectedTotal, mock.writeBuf.Len())
	}
}

func TestAesEmptyWrite(t *testing.T) {
	mock := newMockConn()
	key := make([]byte, 32)
	rand.Read(key)

	wrapped := AesWrapConn(mock, key)

	// Write empty data
	n, err := wrapped.Write([]byte{})
	if err != nil {
		t.Fatalf("Failed to write empty data: %v", err)
	}
	if n != 0 {
		t.Errorf("Expected to write 0 bytes, wrote %d", n)
	}

	// IV should still be sent
	if mock.writeBuf.Len() != 16 {
		t.Errorf("Expected 16 bytes (IV only), got %d", mock.writeBuf.Len())
	}
}

func TestAesConsistentEncryption(t *testing.T) {
	// Same plaintext encrypted twice should produce different ciphertext
	// due to different IVs

	mock1 := newMockConn()
	mock2 := newMockConn()

	key := make([]byte, 32)
	rand.Read(key)

	wrapped1 := AesWrapConn(mock1, key)
	wrapped2 := AesWrapConn(mock2, key)

	testData := []byte("Same plaintext for both")

	wrapped1.Write(testData)
	wrapped2.Write(testData)

	cipher1 := mock1.writeBuf.Bytes()
	cipher2 := mock2.writeBuf.Bytes()

	// Ciphertexts should be different due to different IVs
	if bytes.Equal(cipher1, cipher2) {
		t.Error("Two encryptions of same plaintext produced identical ciphertext (IVs should differ)")
	}
}
