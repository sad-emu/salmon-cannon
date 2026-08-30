package api

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
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
	auth       authenticator
}

// NewServer creates a new API server instance.
func NewServer(cfg *config.SalmonCannonConfig, listenAddr string) *Server {
	return &Server{cfg: cfg, listenAddr: listenAddr}
}

// Start begins listening and serving. It returns after the server has started or an error.
func (s *Server) Start() error {
	tlsConfig, err := s.serverTLSConfig()
	if err != nil {
		return err
	}
	if err := s.loadAuthenticator(); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/bridges", s.handleBridges)
	mux.HandleFunc("/api/v1/status", s.handleStatus)
	var handler http.Handler = mux
	if s.auth != nil {
		handler = s.requireAuthentication(handler)
	}

	h := &http.Server{
		Addr:      s.listenAddr,
		Handler:   handler,
		TLSConfig: tlsConfig,
	}
	s.httpSrv = h

	ln, err := net.Listen("tcp", s.listenAddr)
	if err != nil {
		return err
	}
	s.ln = ln

	go func() {
		var err error
		if tlsConfig != nil {
			log.Printf("api: starting HTTPS server on %s", s.listenAddr)
			err = h.ServeTLS(ln, "", "")
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

func (s *Server) loadAuthenticator() error {
	if s.cfg.ApiConfig == nil {
		return nil
	}
	cfg := s.cfg.ApiConfig
	if cfg.BasicAuthFile != "" && cfg.MTLSAuthFile != "" {
		return errors.New("api: BasicAuthFile and MTLSAuthFile are mutually exclusive")
	}
	if cfg.BasicAuthFile != "" {
		auth, err := loadBasicAuthenticator(cfg.BasicAuthFile)
		if err != nil {
			return fmt.Errorf("api: %w", err)
		}
		s.auth = auth
	}
	if cfg.MTLSAuthFile != "" {
		auth, err := loadMTLSAuthenticator(cfg.MTLSAuthFile)
		if err != nil {
			return fmt.Errorf("api: %w", err)
		}
		s.auth = auth
	}
	return nil
}

func (s *Server) serverTLSConfig() (*tls.Config, error) {
	if s.cfg.ApiConfig == nil {
		return nil, nil
	}
	cfg := s.cfg.ApiConfig
	if (cfg.TLSCert == "") != (cfg.TLSKey == "") {
		return nil, errors.New("api: TLSCert and TLSKey must be configured together")
	}
	if cfg.MTLSAuthFile != "" && (cfg.TLSCert == "" || cfg.TLSClientCA == "") {
		return nil, errors.New("api: MTLSAuthFile requires TLSCert, TLSKey, and TLSClientCA")
	}
	if cfg.TLSClientCA != "" && cfg.MTLSAuthFile == "" {
		return nil, errors.New("api: TLSClientCA requires MTLSAuthFile")
	}
	if cfg.TLSCert == "" {
		return nil, nil
	}

	certificate, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
	if err != nil {
		return nil, fmt.Errorf("api: load TLS certificate: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
	}
	if cfg.MTLSAuthFile != "" {
		pemData, err := os.ReadFile(cfg.TLSClientCA)
		if err != nil {
			return nil, fmt.Errorf("api: read TLS client CA: %w", err)
		}
		clientCAs := x509.NewCertPool()
		if !clientCAs.AppendCertsFromPEM(pemData) {
			return nil, errors.New("api: TLSClientCA contains no valid certificates")
		}
		tlsConfig.ClientCAs = clientCAs
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsConfig, nil
}

func (s *Server) requireAuthentication(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := s.auth.authenticate(r)
		if !result.ok {
			if _, basic := s.auth.(*basicAuthenticator); basic {
				w.Header().Set("WWW-Authenticate", `Basic realm="salmon-cannon-api", charset="UTF-8"`)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, withAuthenticatedUser(r, result.username))
	})
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
	TransferredBytes     uint64  `json:"transferred_bytes"`
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
		transferredBytes := uint64(0)
		if limiterInterface, ok := status.GlobalConnMonitorRef.GetLimiter(b.Name); ok {
			if limiter, ok := limiterInterface.(*limiter.SharedLimiter); ok {
				// GetActiveRate returns bytes per second, convert to bits per second
				activeRateBps = float64(limiter.GetActiveRate()) * 8.0
				transferredBytes = limiter.GetBytesTransferred()
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
			TransferredBytes:     transferredBytes,
		})
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(list); err != nil {
		log.Printf("api: encode error: %v", err)
	}
}
