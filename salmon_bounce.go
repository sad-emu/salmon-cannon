package main

import (
	"context"
	"log"
	"net"
	"sync"
	"time"

	"salmoncannon/config"
)

// SalmonBounce is a user-space UDP relay that forwards packets based on a route map.
// It maintains session state to support bidirectional forwarding without terminating QUIC.
type SalmonBounce struct {
	name        string
	listenAddr  string
	listenConn  *net.UDPConn
	routeMap    map[string]string // client IP → backend address
	idleTimeout time.Duration
	sessions    map[string]*bounceSession
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
}

type bounceSession struct {
	clientAddr  *net.UDPAddr
	backendAddr *net.UDPAddr
	replyConn   *net.UDPConn
	lastSeen    time.Time
	mu          sync.Mutex
}

// NewSalmonBounce creates a new UDP relay instance from config.
func NewSalmonBounce(cfg *config.SalmonBounceConfig) (*SalmonBounce, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &SalmonBounce{
		name:        cfg.Name,
		listenAddr:  cfg.ListenAddr,
		routeMap:    cfg.RouteMap,
		idleTimeout: cfg.IdleTimeout.Duration(),
		sessions:    make(map[string]*bounceSession),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// NewSalmonBounceSimple creates a new UDP relay instance with simple parameters.
// listenAddr should be in form "ip:port" or ":port"
// routeMap maps client IP → backend "ip:port"
func NewSalmonBounceSimple(listenAddr string, routeMap map[string]string) (*SalmonBounce, error) {
	ctx, cancel := context.WithCancel(context.Background())
	return &SalmonBounce{
		name:        "simple-bounce",
		listenAddr:  listenAddr,
		routeMap:    routeMap,
		idleTimeout: 60 * time.Second,
		sessions:    make(map[string]*bounceSession),
		ctx:         ctx,
		cancel:      cancel,
	}, nil
}

// Start begins listening and forwarding UDP packets.
func (b *SalmonBounce) Start() error {
	addr, err := net.ResolveUDPAddr("udp", b.listenAddr)
	if err != nil {
		return err
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	b.listenConn = conn

	log.Printf("SalmonBounce[%s]: listening on %s", b.name, b.listenAddr)

	go b.listenLoop()
	go b.cleanupLoop()

	return nil
}

// Stop gracefully shuts down the bounce server.
func (b *SalmonBounce) Stop() error {
	b.cancel()
	if b.listenConn != nil {
		return b.listenConn.Close()
	}
	return nil
}

// listenLoop reads packets from the listen socket and forwards them.
func (b *SalmonBounce) listenLoop() {
	buf := make([]byte, 65535)
	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		n, clientAddr, err := b.listenConn.ReadFromUDP(buf)
		if err != nil {
			if b.ctx.Err() != nil {
				return
			}
			log.Printf("SalmonBounce: read error: %v", err)
			continue
		}

		// Look up backend for this packet
		backend := b.lookupRoute(clientAddr.IP.String())
		if backend == "" {
			log.Printf("SalmonBounce[%s]: no route for client %s", b.name, clientAddr)
			continue
		}

		// Get or create session
		sess, err := b.getOrCreateSession(clientAddr, backend)
		if err != nil {
			log.Printf("SalmonBounce[%s]: session error: %v", b.name, err)
			continue
		}

		// Forward packet to backend
		sess.mu.Lock()
		_, err = sess.replyConn.WriteToUDP(buf[:n], sess.backendAddr)
		sess.lastSeen = time.Now()
		sess.mu.Unlock()

		if err != nil {
			log.Printf("SalmonBounce[%s]: forward error: %v", b.name, err)
		}
	}
}

// lookupRoute finds the backend address for a given client IP.
func (b *SalmonBounce) lookupRoute(clientIP string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.routeMap[clientIP]
}

// getOrCreateSession returns an existing session or creates a new one.
func (b *SalmonBounce) getOrCreateSession(clientAddr *net.UDPAddr, backend string) (*bounceSession, error) {
	key := clientAddr.String()

	b.mu.RLock()
	sess, exists := b.sessions[key]
	b.mu.RUnlock()

	if exists {
		return sess, nil
	}

	// Create new session
	backendAddr, err := net.ResolveUDPAddr("udp", backend)
	if err != nil {
		return nil, err
	}

	// Create ephemeral UDP socket for this session's replies
	replyConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}

	sess = &bounceSession{
		clientAddr:  clientAddr,
		backendAddr: backendAddr,
		replyConn:   replyConn,
		lastSeen:    time.Now(),
	}

	b.mu.Lock()
	b.sessions[key] = sess
	b.mu.Unlock()

	// Start reply loop for this session
	go b.replyLoop(sess)

	log.Printf("SalmonBounce[%s]: new session %s → %s", b.name, clientAddr, backend)

	return sess, nil
}

// replyLoop reads replies from the backend and forwards them to the client.
func (b *SalmonBounce) replyLoop(sess *bounceSession) {
	buf := make([]byte, 65535)
	defer sess.replyConn.Close()

	for {
		select {
		case <-b.ctx.Done():
			return
		default:
		}

		// Set read deadline to allow periodic checks of context
		sess.replyConn.SetReadDeadline(time.Now().Add(1 * time.Second))

		n, _, err := sess.replyConn.ReadFromUDP(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			if b.ctx.Err() != nil {
				return
			}
			log.Printf("SalmonBounce[%s]: reply read error: %v", b.name, err)
			return
		}

		// Forward reply back to client
		sess.mu.Lock()
		_, err = b.listenConn.WriteToUDP(buf[:n], sess.clientAddr)
		sess.lastSeen = time.Now()
		sess.mu.Unlock()

		if err != nil {
			log.Printf("SalmonBounce[%s]: reply forward error: %v", b.name, err)
		}
	}
}

// cleanupLoop periodically removes stale sessions.
func (b *SalmonBounce) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.cleanupStaleSessions()
		}
	}
}

// cleanupStaleSessions removes sessions that have been idle for too long.
func (b *SalmonBounce) cleanupStaleSessions() {
	now := time.Now()

	b.mu.Lock()
	defer b.mu.Unlock()

	for key, sess := range b.sessions {
		sess.mu.Lock()
		idle := now.Sub(sess.lastSeen)
		sess.mu.Unlock()

		if idle > b.idleTimeout {
			sess.replyConn.Close()
			delete(b.sessions, key)
			log.Printf("SalmonBounce[%s]: cleaned up stale session %s", b.name, key)
		}
	}
}

// AddRoute adds or updates a route in the route map.
func (b *SalmonBounce) AddRoute(clientIP string, backend string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.routeMap[clientIP] = backend
	log.Printf("SalmonBounce[%s]: added route %s → %s", b.name, clientIP, backend)
}

// RemoveRoute removes a route from the route map.
func (b *SalmonBounce) RemoveRoute(clientIP string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.routeMap, clientIP)
	log.Printf("SalmonBounce[%s]: removed route for IP %s", b.name, clientIP)
}
