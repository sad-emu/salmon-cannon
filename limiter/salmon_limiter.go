package limiter

import (
	"net"

	"github.com/juju/ratelimit"
)

const theoreticalMaxBandwidth = 500 * 1024 * 1024 * 1024 // 500 GB/s - lol

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
	bucket  *ratelimit.Bucket
	maxRate int64
}

func NewSharedLimiter(bytesPerSec int64) *SharedLimiter {
	if bytesPerSec <= 0 {
		bytesPerSec = theoreticalMaxBandwidth
	}
	b := ratelimit.NewBucketWithRate(float64(bytesPerSec), bytesPerSec)
	return &SharedLimiter{bucket: b, maxRate: bytesPerSec}
}

// WrapConn wraps a net.Conn so all reads/writes are limited
func (l *SharedLimiter) WrapConn(c net.Conn) net.Conn {
	return &throttledConn{Conn: c, bucket: l.bucket}
}

func (l *SharedLimiter) GetActiveRate() int64 {
	return l.maxRate - l.bucket.Available()
}

func (l *SharedLimiter) GetMaxRate() int64 {
	return l.maxRate
}
