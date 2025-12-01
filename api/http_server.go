package api

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	"salmoncannon/config"
	"salmoncannon/limiter"
	"salmoncannon/status"
)

// Server is a small HTTP API server that serves info about bridges.
// Construct with NewServer(cfg, listenAddr)
type Server struct {
	cfg        *config.SalmonCannonConfig
	listenAddr string
	httpSrv    *http.Server
	ln         net.Listener
}

// NewServer creates a new API server instance.
func NewServer(cfg *config.SalmonCannonConfig, listenAddr string) *Server {
	return &Server{cfg: cfg, listenAddr: listenAddr}
}

// Start begins listening and serving. It returns after the server has started or an error.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/bridges", s.handleBridges)
	mux.HandleFunc("/api/v1/status", s.handleStatus)

	h := &http.Server{
		Addr:    s.listenAddr,
		Handler: mux,
	}
	s.httpSrv = h

	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	s.ln = ln

	// Check if TLS is configured
	useTLS := s.cfg.ApiConfig != nil &&
		s.cfg.ApiConfig.TLSCert != "" &&
		s.cfg.ApiConfig.TLSKey != ""

	go func() {
		var err error
		if useTLS {
			log.Printf("api: starting HTTPS server on %s", s.listenAddr)
			err = h.ServeTLS(ln, s.cfg.ApiConfig.TLSCert, s.cfg.ApiConfig.TLSKey)
		} else {
			log.Printf("api: starting HTTP server on %s", s.listenAddr)
			err = h.Serve(ln)
		}
		if err != nil && err != http.ErrServerClosed {
			log.Printf("api: http server error: %v", err)
		}
	}()

	return nil
}

// Stop attempts a graceful shutdown with a 5s timeout.
func (s *Server) Stop() error {
	if s.httpSrv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.httpSrv.Shutdown(ctx)
}

// bridgeDTO is the JSON shape returned for each bridge
type bridgeDTO struct {
	Name    string `json:"name"`
	Circuit string `json:"circuit"`
	ID      int    `json:"id"`
}

// statusDTO is the JSON shape returned for bandwidth status
type statusDTO struct {
	BridgeName           string  `json:"bridge_name"`
	ActiveStreams        int64   `json:"active_streams"`
	MaxRateBitsPerSec    int64   `json:"max_rate_bps"`
	ActiveRateBitsPerSec float64 `json:"active_rate_bps"`
	LastAliveMin         int64   `json:"last_alive_min"`
	LastPingMs           int64   `json:"last_ping_ms"`
	Alive                bool    `json:"alive"`
}

func (s *Server) handleBridges(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	list := make([]bridgeDTO, 0, len(s.cfg.Bridges))
	for i, b := range s.cfg.Bridges {
		list = append(list, bridgeDTO{Name: b.Name, Circuit: b.Name, ID: i})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		log.Printf("api: encode error: %v", err)
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	list := make([]statusDTO, 0, len(s.cfg.Bridges))

	// Import the status package to access the limiter registry
	// We'll need to iterate through registered limiters
	for _, b := range s.cfg.Bridges {
		maxRateBps := int64(b.TotalBandwidthLimit) * 8 // Convert bytes to bits

		// Try to get the active rate from the registered limiter
		activeRateBps := 0.0
		if limiterInterface, ok := status.GlobalConnMonitorRef.GetLimiter(b.Name); ok {
			if limiter, ok := limiterInterface.(*limiter.SharedLimiter); ok {
				// GetActiveRate returns bytes per second, convert to bits per second
				activeRateBps = float64(limiter.GetActiveRate()) * 8.0
			}
		}

		lastAliveMs := status.GlobalConnMonitorRef.GetLastAliveMs(b.Name)
		if lastAliveMs >= 0 {
			lastAliveMs = lastAliveMs / 60000 // convert to minutes
		}
		lastPingMs := status.GlobalConnMonitorRef.GetPing(b.Name)
		alive := status.GlobalConnMonitorRef.GetStatus(b.Name)
		streamCount := status.GlobalConnMonitorRef.GetStreamCount(b.Name)

		list = append(list, statusDTO{
			BridgeName:           b.Name,
			MaxRateBitsPerSec:    maxRateBps,
			ActiveRateBitsPerSec: activeRateBps,
			Alive:                alive,
			LastAliveMin:         lastAliveMs,
			LastPingMs:           lastPingMs,
			ActiveStreams:        streamCount,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		log.Printf("api: encode error: %v", err)
	}
}
