package api

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"time"

	"salmoncannon/config"
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

	go func() {
		if err := h.Serve(ln); err != nil && err != http.ErrServerClosed {
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
