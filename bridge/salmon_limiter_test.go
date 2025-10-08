package bridge

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/juju/ratelimit"
)

// fakeConn implements net.Conn for testing.
type fakeConn struct {
	readBuf  *bytes.Buffer
	writeBuf *bytes.Buffer
	closed   bool
}

func newFakeConn(data string) *fakeConn {
	return &fakeConn{
		readBuf:  bytes.NewBufferString(data),
		writeBuf: &bytes.Buffer{},
	}
}

func (f *fakeConn) Read(p []byte) (int, error)         { return f.readBuf.Read(p) }
func (f *fakeConn) Write(p []byte) (int, error)        { return f.writeBuf.Write(p) }
func (f *fakeConn) Close() error                       { f.closed = true; return nil }
func (f *fakeConn) LocalAddr() net.Addr                { return nil }
func (f *fakeConn) RemoteAddr() net.Addr               { return nil }
func (f *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (f *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (f *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// ---- Tests for throttledConn ----

func TestThrottledConn_Read_Pass(t *testing.T) {
	bucket := ratelimit.NewBucketWithRate(1e6, 1e6) // high rate, shouldn't block
	fc := newFakeConn("hello world")
	tc := &throttledConn{Conn: fc, bucket: bucket}

	buf := make([]byte, 11)
	n, err := tc.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", string(buf[:n]))
	}
}

func TestThrottledConn_Read_Empty(t *testing.T) {
	bucket := ratelimit.NewBucketWithRate(1e6, 1e6)
	fc := newFakeConn("") // no data to read
	tc := &throttledConn{Conn: fc, bucket: bucket}

	buf := make([]byte, 1)
	n, err := tc.Read(buf)
	if n != 0 || err != io.EOF {
		t.Errorf("expected EOF and 0 bytes, got n=%d, err=%v", n, err)
	}
}

func TestThrottledConn_Write_Pass(t *testing.T) {
	bucket := ratelimit.NewBucketWithRate(1e6, 1e6)
	fc := newFakeConn("")
	tc := &throttledConn{Conn: fc, bucket: bucket}

	data := []byte("foobar")
	n, err := tc.Write(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected to write %d bytes, wrote %d", len(data), n)
	}
	if fc.writeBuf.String() != "foobar" {
		t.Errorf("expected 'foobar' in writeBuf, got '%s'", fc.writeBuf.String())
	}
}

func TestThrottledConn_Write_Zero(t *testing.T) {
	bucket := ratelimit.NewBucketWithRate(1e6, 1e6)
	fc := newFakeConn("")
	tc := &throttledConn{Conn: fc, bucket: bucket}

	n, err := tc.Write([]byte{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 0 {
		t.Errorf("expected to write 0 bytes, wrote %d", n)
	}
}

// // ---- Tests for SharedLimiter ----

func TestSharedLimiter_WrapConn(t *testing.T) {
	sl := NewSharedLimiter(1e6)
	fc := newFakeConn("abc")
	conn := sl.WrapConn(fc)

	// Write test
	n, err := conn.Write([]byte("xyz"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("expected to write 3 bytes, wrote %d", n)
	}
	if fc.writeBuf.String() != "xyz" {
		t.Errorf("expected 'xyz' in writeBuf, got '%s'", fc.writeBuf.String())
	}

	// Read test
	buf := make([]byte, 3)
	n, err = conn.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(buf[:n]) != "abc" {
		t.Errorf("expected 'abc', got '%s'", string(buf[:n]))
	}
}

func TestNewSharedLimiter_NegativeZero(t *testing.T) {
	// Anything below zero retuns nil
	sl := NewSharedLimiter(0)
	if sl != nil && sl.bucket != nil {
		t.Fatal("expected non-nil SharedLimiter and bucket")
	}
}
