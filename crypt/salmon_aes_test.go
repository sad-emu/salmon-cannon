package crypt

// import (
// 	"bytes"
// 	"crypto/aes"
// 	"crypto/cipher"
// 	"crypto/rand"
// 	"encoding/base64"
// 	"io"
// 	"salmoncannon/utils"
// 	"testing"
// 	"time"

// 	"github.com/quic-go/quic-go"
// )

// // mockQuicStream implements quic.Stream for testing
// type mockQuicStream struct {
// 	readBuf   *bytes.Buffer
// 	writeBuf  *bytes.Buffer
// 	closed    bool
// 	streamID  quic.StreamID
// 	readErr   error
// 	writeErr  error
// 	cancelErr error
// }

// func newMockQuicStream() *mockQuicStream {
// 	return &mockQuicStream{
// 		readBuf:  &bytes.Buffer{},
// 		writeBuf: &bytes.Buffer{},
// 		streamID: quic.StreamID(1),
// 	}
// }

// func (m *mockQuicStream) Read(b []byte) (int, error) {
// 	if m.closed {
// 		return 0, io.EOF
// 	}
// 	if m.readErr != nil {
// 		return 0, m.readErr
// 	}
// 	return m.readBuf.Read(b)
// }

// func (m *mockQuicStream) Write(b []byte) (int, error) {
// 	if m.closed {
// 		return 0, io.ErrClosedPipe
// 	}
// 	if m.writeErr != nil {
// 		return 0, m.writeErr
// 	}
// 	return m.writeBuf.Write(b)
// }

// func (m *mockQuicStream) Close() error {
// 	m.closed = true
// 	return nil
// }

// func (m *mockQuicStream) CancelRead(code quic.StreamErrorCode) {
// 	m.cancelErr = io.EOF
// }

// func (m *mockQuicStream) CancelWrite(code quic.StreamErrorCode) {
// 	m.cancelErr = io.ErrClosedPipe
// }

// func (m *mockQuicStream) SetReadDeadline(t time.Time) error {
// 	return nil
// }

// func (m *mockQuicStream) SetWriteDeadline(t time.Time) error {
// 	return nil
// }

// func (m *mockQuicStream) SetDeadline(t time.Time) error {
// 	return nil
// }

// func (m *mockQuicStream) StreamID() quic.StreamID {
// 	return m.streamID
// }

// func TestAesWrapQuicStream(t *testing.T) {
// 	mock := newMockQuicStream()
// 	key := make([]byte, 32)
// 	rand.Read(key)
// 	keyStr := base64.StdEncoding.EncodeToString(key)

// 	stream := quic.Stream(mock)
// 	wrapped := AesWrapQuicStream(&stream, keyStr)
// 	if wrapped == nil {
// 		t.Fatal("AesWrapQuicStream returned nil")
// 	}
// 	if wrapped.Stream == nil {
// 		t.Error("Wrapped stream is nil")
// 	}
// 	if wrapped.sharedSecret != keyStr {
// 		t.Error("Shared secret not set correctly")
// 	}
// }

// func TestAesEncryptDecrypt(t *testing.T) {
// 	clientToServer := newMockQuicStream()
// 	serverToClient := newMockQuicStream()

// 	key := make([]byte, 32)
// 	rand.Read(key)
// 	keyStr := base64.StdEncoding.EncodeToString(key)

// 	clientStream := quic.Stream(clientToServer)
// 	serverStream := quic.Stream(serverToClient)
// 	clientConn := AesWrapQuicStream(&clientStream, keyStr)
// 	serverConn := AesWrapQuicStream(&serverStream, keyStr)

// 	testData := []byte("Hello, World! This is a test message.")

// 	n, err := clientConn.Write(testData)
// 	if err != nil {
// 		t.Fatalf("Client write failed: %v", err)
// 	}
// 	if n != len(testData) {
// 		t.Fatalf("Client write: expected %d bytes, got %d", len(testData), n)
// 	}

// 	serverToClient.readBuf = bytes.NewBuffer(clientToServer.writeBuf.Bytes())

// 	readBuf := make([]byte, len(testData))
// 	n, err = serverConn.Read(readBuf)
// 	if err != nil {
// 		t.Fatalf("Server read failed: %v", err)
// 	}
// 	if n != len(testData) {
// 		t.Fatalf("Server read: expected %d bytes, got %d", len(testData), n)
// 	}

// 	if !bytes.Equal(readBuf[:n], testData) {
// 		t.Fatalf("Decrypted data doesn't match original.\nExpected: %s\nGot: %s", testData, readBuf[:n])
// 	}
// }

// func TestAesLargeData(t *testing.T) {
// 	mock := newMockQuicStream()
// 	key := make([]byte, 32)
// 	rand.Read(key)
// 	keyStr := base64.StdEncoding.EncodeToString(key)

// 	stream := quic.Stream(mock)
// 	wrapped := AesWrapQuicStream(&stream, keyStr)

// 	largeData := make([]byte, 64)
// 	rand.Read(largeData)

// 	n, err := wrapped.Write(largeData)
// 	if err != nil {
// 		t.Fatalf("Failed to write large data: %v", err)
// 	}
// 	if n != len(largeData) {
// 		t.Fatalf("Expected to write %d bytes, wrote %d", len(largeData), n)
// 	}

// 	expectedTotal := keyRandomHashSize + 16 + len(largeData)
// 	if mock.writeBuf.Len() != expectedTotal {
// 		t.Errorf("Expected %d total bytes, got %d", expectedTotal, mock.writeBuf.Len())
// 	}

// 	encryptedData := mock.writeBuf.Bytes()[keyRandomHashSize+16:]
// 	keyMod := mock.writeBuf.Bytes()[:keyRandomHashSize]
// 	iv := mock.writeBuf.Bytes()[keyRandomHashSize : keyRandomHashSize+16]
// 	deckey, _ := utils.DeriveEncKeyFromBytesAndSalt(keyStr, keyMod)

// 	block, err := aes.NewCipher(deckey)
// 	if err != nil {
// 		t.Fatalf("Failed to create cipher for decryption: %v", err)
// 	}

// 	ctr := cipher.NewCTR(block, iv)
// 	decryptedData := make([]byte, len(encryptedData))
// 	ctr.XORKeyStream(decryptedData, encryptedData)

// 	if !bytes.Equal(decryptedData, largeData) {
// 		t.Error("Decrypted large data does not match original")
// 	}
// }
