package main

import (
	"net"

	"github.com/juju/ratelimit"
)

// throttledConn wraps net.Conn and applies a bandwidth limit on Read and Write
type throttledConn struct {
	net.Conn
	bucket *ratelimit.Bucket
}

func (t *throttledConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.bucket.Wait(int64(n))
	}
	return n, err
}

func (t *throttledConn) Write(p []byte) (int, error) {
	t.bucket.Wait(int64(len(p)))
	return t.Conn.Write(p)
}

// SharedLimiter provides a global limiter and a way to wrap net.Conn
type SharedLimiter struct {
	bucket *ratelimit.Bucket
}

func NewSharedLimiter(bytesPerSec int64) *SharedLimiter {
	if bytesPerSec <= 0 {
		return nil
	}
	b := ratelimit.NewBucketWithRate(float64(bytesPerSec), bytesPerSec)
	return &SharedLimiter{bucket: b}
}

// WrapConn wraps a net.Conn so all reads/writes are limited
func (l *SharedLimiter) WrapConn(c net.Conn) net.Conn {
	return &throttledConn{Conn: c, bucket: l.bucket}
}
