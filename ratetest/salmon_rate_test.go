package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"strings"
	"testing"
)

func encodeRateTest(t *testing.T, chunks int, corrupt bool) ([]byte, rateTestResult) {
	t.Helper()

	var stream bytes.Buffer
	if err := writeAll(&stream, []byte(rateProtocolMagic)); err != nil {
		t.Fatalf("write protocol magic: %v", err)
	}

	payload := make([]byte, rateChunkSize)
	var sent rateTestResult
	for sequence := 0; sequence < chunks; sequence++ {
		benchmarkPayload(uint64(sequence), payload)
		if err := writeRateFrameHeader(&stream, rateFrameData, uint64(sequence), uint32(len(payload))); err != nil {
			t.Fatalf("write data header: %v", err)
		}
		if err := writeAll(&stream, payload); err != nil {
			t.Fatalf("write data payload: %v", err)
		}
		sent.checksum = crc32.Update(sent.checksum, rateCRCTable, payload)
		sent.bytes += uint64(len(payload))
		sent.chunks++
	}

	encoded := stream.Bytes()
	if corrupt {
		encoded[len(rateProtocolMagic)+rateFrameHeader+123] ^= 0x80
	}

	var completion [rateEndPayload]byte
	binary.BigEndian.PutUint64(completion[0:8], sent.bytes)
	binary.BigEndian.PutUint32(completion[8:12], sent.checksum)
	if err := writeRateFrameHeader(&stream, rateFrameEnd, sent.chunks, rateEndPayload); err != nil {
		t.Fatalf("write completion header: %v", err)
	}
	if err := writeAll(&stream, completion[:]); err != nil {
		t.Fatalf("write completion payload: %v", err)
	}
	return stream.Bytes(), sent
}

func TestReceiveRateTestAndAcknowledgement(t *testing.T) {
	encoded, sent := encodeRateTest(t, 3, false)
	received, err := receiveRateTest(bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("receiveRateTest: %v", err)
	}
	if received.bytes != sent.bytes || received.chunks != sent.chunks || received.checksum != sent.checksum {
		t.Fatalf("received result = %+v, want %+v", received, sent)
	}
	if received.duration <= 0 {
		t.Fatalf("receiver duration = %s, want positive", received.duration)
	}

	var ack bytes.Buffer
	if err := writeRateAcknowledgement(&ack, received, nil); err != nil {
		t.Fatalf("writeRateAcknowledgement: %v", err)
	}
	confirmed, err := readRateAcknowledgement(&ack)
	if err != nil {
		t.Fatalf("readRateAcknowledgement: %v", err)
	}
	if confirmed != received {
		t.Fatalf("confirmed result = %+v, want %+v", confirmed, received)
	}
}

func TestReceiveRateTestRejectsCorruption(t *testing.T) {
	encoded, _ := encodeRateTest(t, 1, true)
	_, err := receiveRateTest(bytes.NewReader(encoded))
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("receiveRateTest error = %v, want checksum mismatch", err)
	}
}

func TestRateAcknowledgementPropagatesReceiverFailure(t *testing.T) {
	var ack bytes.Buffer
	want := errors.New("integrity check failed")
	if err := writeRateAcknowledgement(&ack, rateTestResult{bytes: 123}, want); err != nil {
		t.Fatalf("writeRateAcknowledgement: %v", err)
	}
	result, err := readRateAcknowledgement(&ack)
	if result.bytes != 123 {
		t.Fatalf("acknowledged bytes = %d, want 123", result.bytes)
	}
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("readRateAcknowledgement error = %v, want %q", err, want)
	}
}

type shortWriter struct {
	bytes.Buffer
	limit int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		p = p[:w.limit]
	}
	return w.Buffer.Write(p)
}

func TestWriteAllHandlesShortWrites(t *testing.T) {
	w := &shortWriter{limit: 3}
	want := []byte("receiver-confirmed")
	if err := writeAll(w, want); err != nil {
		t.Fatalf("writeAll: %v", err)
	}
	if !bytes.Equal(w.Bytes(), want) {
		t.Fatalf("written bytes = %q, want %q", w.Bytes(), want)
	}
}
