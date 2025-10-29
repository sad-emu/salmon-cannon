package main

import (
	"log"
	"runtime"
	"sync/atomic"
	"time"
)

// ConnectionMonitor tracks active connections for debugging
type ConnectionMonitor struct {
	activeSOCKS atomic.Int64
	activeHTTP  atomic.Int64
	totalSOCKS  atomic.Int64
	totalHTTP   atomic.Int64
}

var globalConnMonitor = &ConnectionMonitor{}

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

func (cm *ConnectionMonitor) StartPeriodicLogging() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()

		for range ticker.C {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)

			log.Printf("MONITOR: Active connections - SOCKS: %d, HTTP: %d | Total served - SOCKS: %d, HTTP: %d | Goroutines: %d | HeapAlloc: %d MB",
				cm.activeSOCKS.Load(),
				cm.activeHTTP.Load(),
				cm.totalSOCKS.Load(),
				cm.totalHTTP.Load(),
				runtime.NumGoroutine(),
				m.HeapAlloc/1024/1024,
			)
		}
	}()
}
