package main

import (
	"crypto/tls"
	"fmt"
	"net"

	quic "github.com/quic-go/quic-go"
)

type SalmonFar struct {
	port      int
	ln        net.Listener
	farBridge SalmonBridge
}

// TODO - for bridge types it should start listeners for them
// near should be able to make requests through them

func NewSalmonFar(port int) (*SalmonFar, error) {
	far := &SalmonFar{
		port:      port,
		farBridge: SalmonBridge{},
	}
	far.farBridge.tlscfg = &tls.Config{
		Certificates: []tls.Certificate{generateSelfSignedCert()},
		NextProtos:   []string{"salmon-bridge"},
	}

	// TODO is this bits or bytes?
	far.farBridge.sl = NewSharedLimiter(1024 * 1024 * 100)

	far.farBridge.qcfg = &quic.Config{
		// Tune as needed (see near side).
	}

	far.farBridge.bridgeDown = true
	farListenAddr := fmt.Sprintf(":%d", port)
	fmt.Printf("farListenAddr: '%s' (len=%d)\n", farListenAddr, len(farListenAddr))
	far.farBridge.NewFarListen(farListenAddr)
	return far, nil
}
