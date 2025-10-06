package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// --- encodeFrame Tests ---

func TestEncodeFrame_Basic(t *testing.T) {
	frame := Frame{
		Type:   MsgData,
		ConnID: 0x12345678,
		Data:   []byte("hello"),
	}
	encoded := encodeFrame(frame)
	if len(encoded) != 9+5 {
		t.Fatalf("expected encoded len 14, got %d", len(encoded))
	}
	if encoded[0] != byte(MsgData) {
		t.Errorf("expected type %d, got %d", MsgData, encoded[0])
	}
	connID := binary.BigEndian.Uint32(encoded[1:5])
	if connID != 0x12345678 {
		t.Errorf("expected connID 0x12345678, got 0x%x", connID)
	}
	dlen := binary.BigEndian.Uint32(encoded[5:9])
	if dlen != 5 {
		t.Errorf("expected data len 5, got %d", dlen)
	}
	if string(encoded[9:]) != "hello" {
		t.Errorf("expected data 'hello', got %q", string(encoded[9:]))
	}
}

func TestEncodeFrame_EmptyData(t *testing.T) {
	frame := Frame{
		Type:   MsgOpen,
		ConnID: 42,
		Data:   nil,
	}
	encoded := encodeFrame(frame)
	if len(encoded) != 9 {
		t.Errorf("expected encoded len 9, got %d", len(encoded))
	}
	if encoded[0] != byte(MsgOpen) {
		t.Errorf("expected type %d, got %d", MsgOpen, encoded[0])
	}
	connID := binary.BigEndian.Uint32(encoded[1:5])
	if connID != 42 {
		t.Errorf("expected connID 42, got %d", connID)
	}
	dlen := binary.BigEndian.Uint32(encoded[5:9])
	if dlen != 0 {
		t.Errorf("expected data len 0, got %d", dlen)
	}
}

// --- decodeFrame Tests ---

func TestDecodeFrame_Basic(t *testing.T) {
	orig := Frame{Type: MsgData, ConnID: 99, Data: []byte("abc")}
	buf := bytes.NewBuffer(encodeFrame(orig))
	got, err := decodeFrame(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != MsgData {
		t.Errorf("expected type %d, got %d", MsgData, got.Type)
	}
	if got.ConnID != 99 {
		t.Errorf("expected ConnID 99, got %d", got.ConnID)
	}
	if string(got.Data) != "abc" {
		t.Errorf("expected data 'abc', got %q", got.Data)
	}
}

func TestDecodeFrame_EmptyData(t *testing.T) {
	orig := Frame{Type: MsgClose, ConnID: 7, Data: []byte{}}
	buf := bytes.NewBuffer(encodeFrame(orig))
	got, err := decodeFrame(buf)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != MsgClose {
		t.Errorf("expected MsgClose, got %d", got.Type)
	}
	if got.ConnID != 7 {
		t.Errorf("expected ConnID 7, got %d", got.ConnID)
	}
	if len(got.Data) != 0 {
		t.Errorf("expected empty data, got len %d", len(got.Data))
	}
}

func TestDecodeFrame_ShortHeader(t *testing.T) {
	bad := bytes.NewBuffer([]byte{1, 2, 3})
	_, err := decodeFrame(bad)
	if err == nil {
		t.Fatal("expected error for short header, got nil")
	}
}

func TestDecodeFrame_ShortData(t *testing.T) {
	// header says 4 bytes data, only provide 2
	header := []byte{byte(MsgData), 0, 0, 0, 1, 0, 0, 0, 4}
	buf := bytes.NewBuffer(append(header, []byte("xy")...))
	_, err := decodeFrame(buf)
	if err == nil {
		t.Fatal("expected error for short data, got nil")
	}
}

func TestEncodeDecodeFrame_Roundtrip(t *testing.T) {
	frames := []Frame{
		{Type: MsgOpen, ConnID: 1, Data: []byte("foo")},
		{Type: MsgData, ConnID: 2, Data: []byte{}},
		{Type: MsgClose, ConnID: 3, Data: []byte("bye")},
	}
	for _, f := range frames {
		buf := bytes.NewBuffer(encodeFrame(f))
		got, err := decodeFrame(buf)
		if err != nil {
			t.Fatalf("roundtrip error: %v", err)
		}
		if got.Type != f.Type || got.ConnID != f.ConnID || !bytes.Equal(got.Data, f.Data) {
			t.Errorf("mismatch: in=%#v got=%#v", f, got)
		}
	}
}
