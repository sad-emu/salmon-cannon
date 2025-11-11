package limiter

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/juju/ratelimit"
)

const theoreticalMaxBandwidth = 500 * 1024 * 1024 * 1024 // 500 GB/s - lol
const numBuckets = 5                                     // 5 one-second buckets for 5-second window

// throttledConn wraps net.Conn and applies a bandwidth limit on Read and Write
type throttledConn struct {
	net.Conn
	bucket  *ratelimit.Bucket
	limiter *SharedLimiter
}

func (t *throttledConn) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.bucket.Wait(int64(n))
		if t.limiter != nil {
			t.limiter.recordBytes(int64(n))
		}
	}
	return n, err
}

func (t *throttledConn) Write(p []byte) (int, error) {
	t.bucket.Wait(int64(len(p)))
	n, err := t.Conn.Write(p)
	if err == nil {
		if t.limiter != nil {
			t.limiter.recordBytes(int64(n))
		}
	}
	return n, err
}

// timeBucket holds bytes for a 1-second window
type timeBucket struct {
	bytes     int64 // atomic
	timestamp int64 // atomic, unix timestamp
}

// SharedLimiter provides a global limiter and a way to wrap net.Conn
type SharedLimiter struct {
	bucket     *ratelimit.Bucket
	maxRate    int64
	buckets    [numBuckets]timeBucket
	currentIdx int64 // atomic, current bucket index
	lastRotate int64 // atomic, last rotation unix timestamp
	windowSize time.Duration
}

func NewSharedLimiter(bytesPerSec int64) *SharedLimiter {
	if bytesPerSec <= 0 {
		bytesPerSec = theoreticalMaxBandwidth
	}
	b := ratelimit.NewBucketWithRate(float64(bytesPerSec), bytesPerSec)
	now := time.Now().Unix()
	sl := &SharedLimiter{
		bucket:     b,
		maxRate:    bytesPerSec,
		windowSize: 5 * time.Second,
		lastRotate: now,
		currentIdx: 0,
	}
	// Initialize all buckets with current timestamp
	for i := range sl.buckets {
		atomic.StoreInt64(&sl.buckets[i].timestamp, now)
	}
	return sl
}

// recordBytes records bytes transferred (lock-free, fast path)
func (l *SharedLimiter) recordBytes(n int64) {
	now := time.Now().Unix()
	lastRotate := atomic.LoadInt64(&l.lastRotate)

	// Rotate bucket if we've moved to a new second
	if now > lastRotate {
		// Try to rotate (only one goroutine will succeed)
		if atomic.CompareAndSwapInt64(&l.lastRotate, lastRotate, now) {
			// Advance to next bucket
			currentIdx := atomic.LoadInt64(&l.currentIdx)
			nextIdx := (currentIdx + 1) % numBuckets
			atomic.StoreInt64(&l.currentIdx, nextIdx)

			// Reset the new current bucket
			atomic.StoreInt64(&l.buckets[nextIdx].bytes, 0)
			atomic.StoreInt64(&l.buckets[nextIdx].timestamp, now)
		}
	}

	// Add bytes to current bucket (atomic add is very fast)
	idx := atomic.LoadInt64(&l.currentIdx)
	atomic.AddInt64(&l.buckets[idx].bytes, n)
}

// WrapConn wraps a net.Conn so all reads/writes are limited
func (l *SharedLimiter) WrapConn(c net.Conn) net.Conn {
	return &throttledConn{Conn: c, bucket: l.bucket, limiter: l}
}

func (l *SharedLimiter) GetActiveRate() int64 {
	now := time.Now().Unix()
	cutoff := now - int64(l.windowSize.Seconds())

	var totalBytes int64
	var oldestTimestamp int64 = now

	// Sum up all buckets within the window
	for i := 0; i < numBuckets; i++ {
		ts := atomic.LoadInt64(&l.buckets[i].timestamp)
		if ts >= cutoff {
			bytes := atomic.LoadInt64(&l.buckets[i].bytes)
			totalBytes += bytes
			if ts < oldestTimestamp {
				oldestTimestamp = ts
			}
		}
	}

	// Calculate rate based on actual time span
	duration := now - oldestTimestamp
	if duration > 0 {
		return totalBytes / duration
	}

	return 0
}

func (l *SharedLimiter) GetMaxRate() int64 {
	return l.maxRate
}
