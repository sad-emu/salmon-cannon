package bridge

import (
	"bytes"
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
	if len(data) < 2 {
		t.Fatalf("buffer too short: %v", data)
	}
	l := int(data[0])<<8 | int(data[1])
	if l != len(addr) {
		t.Errorf("expected length %d, got %d", len(addr), l)
	}
	if string(data[2:]) != addr {
		t.Errorf("expected addr %q, got %q", addr, string(data[2:]))
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

	got, mode, err := ReadTargetHeader(buf)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if got != addr {
		t.Errorf("expected addr %q, got %q", addr, got)
	}
	if mode != tcpHeader {
		t.Errorf("expected mode %d, got %d", tcpHeader, mode)
	}
}

func TestReadTargetHeader_EmptyInput(t *testing.T) {
	// Write a buffer with length 0 in the header
	buf := &bytes.Buffer{}
	buf.Write([]byte{0, 0})
	_, mode, err := ReadTargetHeader(buf)
	if err == nil || err.Error() != "empty target" || mode != 0x0 {
		t.Fatalf("expected 'empty target' error, got: %v", err)
	}
}

func TestReadTargetHeader_EarlyEOF(t *testing.T) {
	// Not enough bytes for length header
	buf := &bytes.Buffer{}
	buf.Write([]byte{0x00})
	_, _, err := ReadTargetHeader(buf)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
