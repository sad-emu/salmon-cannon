package bridge

import (
	"bytes"
	"crypto/rand"
	"testing"
)

// =========================
// WriteTargetHeader TESTS
// =========================

func TestWriteTargetHeader_ValidInput(t *testing.T) {
	buf := &bytes.Buffer{}
	addr := "localhost:8080"
	err := WriteTargetHeader(buf, addr)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	// Should be 2 bytes length, then bytes of addr
	data := buf.Bytes()
	if len(data) < 3 {
		t.Fatalf("buffer too short: %v", data)
	}
	l := int(data[1])<<8 | int(data[2])
	if l != len(addr) {
		t.Errorf("expected length %d, got %d", len(addr), l)
	}
	if string(data[3:]) != addr {
		t.Errorf("expected addr %q, got %q", addr, string(data[3:]))
	}
}

func TestWriteTargetHeader_TooLongInput(t *testing.T) {
	buf := &bytes.Buffer{}
	addr := make([]byte, 70000) // > 65535
	err := WriteTargetHeader(buf, string(addr))
	if err == nil || err.Error() != "target address too long" {
		t.Fatalf("expected 'target address too long' error, got: %v", err)
	}
}

// =========================
// ReadTargetHeader TESTS
// =========================

func TestReadTargetHeader_ValidInput(t *testing.T) {
	addr := "localhost:9090"
	buf := &bytes.Buffer{}
	_ = WriteTargetHeader(buf, addr)

	got1, err1 := ReadHeaderType(buf)
	if err1 != nil {
		t.Fatalf("expected nil err, got %v", err1)
	}
	if got1 != CONNECT_HEADER {
		t.Errorf("expected header type %d, got %d", CONNECT_HEADER, got1)
	}

	got, err := ReadTargetHeader(buf)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != addr {
		t.Errorf("expected addr %q, got %q", addr, got)
	}
}

func TestReadTargetHeader_EmptyInput(t *testing.T) {
	// Write a buffer with length 0 in the header
	buf := &bytes.Buffer{}
	buf.Write([]byte{0, 0})
	_, err := ReadTargetHeader(buf)
	if err == nil || err.Error() != "empty target" {
		t.Fatalf("expected 'empty target' error, got: %v", err)
	}
}

func TestReadTargetHeader_EarlyEOF(t *testing.T) {
	// Not enough bytes for length header
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00})
	_, err := ReadTargetHeader(buf)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestWriteTargetHeader_ValidInputEncrypted(t *testing.T) {
	buf := &bytes.Buffer{}
	addr := "localhost:8080"

	readIv := make([]byte, 16)
	writeIv := make([]byte, 16)
	readKey := make([]byte, 32)
	writeKey := make([]byte, 32)
	rand.Read(readIv)
	rand.Read(writeIv)
	rand.Read(readKey)
	rand.Read(writeKey)

	err := WriteTargetHeaderEnc(buf, addr, readIv, writeIv, readKey, writeKey, "sharedSecret")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	// Should be 2 bytes length, then bytes of addr
	data := buf.Bytes()
	if len(data) < 3 {
		t.Fatalf("buffer too short: %v", data)
	}
	l := int(data[1])<<8 | int(data[2])
	if l != 110 {
		t.Errorf("expected encrypted length %d, got %d", 110, l)
	}
	if bytes.Contains(data[1:], []byte(addr)) {
		t.Errorf("Buffer contains plaintext addr %q, got %q bytes matching are %q", addr, string(data[3:]), []byte(addr))
	}

	if bytes.Contains(data[1:], readIv) {
		t.Errorf("Buffer contains plaintext readIv %v", readIv)
	}
	if bytes.Contains(data[1:], writeIv) {
		t.Errorf("Buffer contains plaintext writeIv %v", writeIv)
	}
	if bytes.Contains(data[1:], readKey) {
		t.Errorf("Buffer contains plaintext readKey %v", readKey)
	}
	if bytes.Contains(data[1:], writeKey) {
		t.Errorf("Buffer contains plaintext writeKey %v", writeKey)
	}

	decryptedAddr, outReadIv, outWriteIv, outReadKey, outWriteKey, err := ReadTargetHeaderEnc(bytes.NewReader(data[1:]), "sharedSecret")
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if decryptedAddr != addr {
		t.Errorf("expected decrypted addr %q, got %q", addr, decryptedAddr)
	}

	if !bytes.Equal(outReadIv, readIv) {
		t.Errorf("readIv mismatch")
	}
	if !bytes.Equal(outWriteIv, writeIv) {
		t.Errorf("writeIv mismatch")
	}
	if !bytes.Equal(outReadKey, readKey) {
		t.Errorf("readKey mismatch")
	}
	if !bytes.Equal(outWriteKey, writeKey) {
		t.Errorf("writeKey mismatch")
	}
}
