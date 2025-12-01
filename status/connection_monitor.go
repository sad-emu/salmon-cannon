package status

import (
	"log"
	"runtime"
	"salmoncannon/limiter"
	"sync"
	"sync/atomic"
	"time"
)

// ConnectionMonitor tracks active connections for debugging
type ConnectionMonitor struct {
	activeSOCKS atomic.Int64
	activeHTTP  atomic.Int64
	activeOUT   atomic.Int64
	totalSOCKS  atomic.Int64
	totalHTTP   atomic.Int64
	totalOUT    atomic.Int64

	limiterMap sync.Map
	statusMap  sync.Map
	streamMap  sync.Map
	pingMap    sync.Map
}

var GlobalConnMonitorRef = &ConnectionMonitor{}

func (cm *ConnectionMonitor) RegisterLimiter(name string, limiter *limiter.SharedLimiter) {
	cm.limiterMap.Store(name, limiter)
}

func (cm *ConnectionMonitor) GetLimiter(name string) (interface{}, bool) {
	return cm.limiterMap.Load(name)
}

func (cm *ConnectionMonitor) RegisterPing(name string, ping int64) {
	cm.statusMap.Store(name, time.Now())
	cm.pingMap.Store(name, ping)
}

func (cm *ConnectionMonitor) AddStream(bridgeName string) {
	pval, _ := cm.streamMap.LoadOrStore(bridgeName, int64(0))
	cm.streamMap.Store(bridgeName, pval.(int64)+1)
}

func (cm *ConnectionMonitor) RemoveStream(bridgeName string) {
	pval, _ := cm.streamMap.LoadOrStore(bridgeName, int64(0))
	cm.streamMap.Store(bridgeName, pval.(int64)-1)
}

func (cm *ConnectionMonitor) ResetStreamCount(bridgeName string) {
	cm.streamMap.LoadOrStore(bridgeName, int64(0))
	cm.streamMap.Store(bridgeName, int64(0))
}

func (cm *ConnectionMonitor) GetStreamCount(bridgeName string) int64 {
	pval, ok := cm.streamMap.Load(bridgeName)
	if !ok {
		return 0
	}
	return pval.(int64)
}

func (cm *ConnectionMonitor) GetStatus(name string) bool {
	lastStatusTime, ok := cm.statusMap.Load(name)
	if !ok {
		return ok
	}
	return ok && time.Since(lastStatusTime.(time.Time)) < 20*time.Second
}

func (cm *ConnectionMonitor) GetLastAliveMs(name string) int64 {
	lastStatusTime, exists := cm.statusMap.Load(name)
	if !exists {
		return -1
	}
	return time.Since(lastStatusTime.(time.Time)).Milliseconds()
}

func (cm *ConnectionMonitor) GetPing(name string) int64 {
	ping, exists := cm.pingMap.Load(name)
	if !exists {
		return -1
	}
	return ping.(int64)
}

func (cm *ConnectionMonitor) IncSOCKS() {
	cm.activeSOCKS.Add(1)
	cm.totalSOCKS.Add(1)
}

func (cm *ConnectionMonitor) DecSOCKS() {
	cm.activeSOCKS.Add(-1)
}

func (cm *ConnectionMonitor) IncHTTP() {
	cm.activeHTTP.Add(1)
	cm.totalHTTP.Add(1)
}

func (cm *ConnectionMonitor) DecHTTP() {
	cm.activeHTTP.Add(-1)
}

func (cm *ConnectionMonitor) IncOUT() {
	cm.activeOUT.Add(1)
	cm.totalOUT.Add(1)
}

func (cm *ConnectionMonitor) DecOUT() {
	cm.activeOUT.Add(-1)
}

func (cm *ConnectionMonitor) StartPeriodicLogging() {
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			log.Printf("MONITOR: Active connections - SOCKS: %d, HTTP: %d, OUT: %d | Total served - SOCKS: %d, HTTP: %d, OUT: %d | Goroutines: %d | HeapAlloc: %d MB",
				cm.activeSOCKS.Load(),
				cm.activeHTTP.Load(),
				cm.activeOUT.Load(),
				cm.totalSOCKS.Load(),
				cm.totalHTTP.Load(),
				cm.totalOUT.Load(),
				runtime.NumGoroutine(),
				m.HeapAlloc/1024/1024,
			)

			// cm.limiterMap.Range(func(key, value interface{}) bool {
			// 	name := key.(string)
			// 	limiter := value.(*limiter.SharedLimiter)
			// 	activeRate := (float64(limiter.GetActiveRate()) / 1024.0 / 1024.0) * 8.0
			// 	log.Printf("MONITOR: Current rate for bridge %s - %.2f mbps",
			// 		name, activeRate)
			// 	return true
			// })
		}
	}()
}
