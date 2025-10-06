package main

import (
	"fmt"
	"net"
)

type SalmonFar struct {
	port      int
	ln        net.Listener
	farBridge SalmonTCPBridge
}

// TODO - for bridge types it should start listeners for them
// near should be able to make requests through them

func NewSalmonFar(port int) (*SalmonFar, error) {
	far := &SalmonFar{
		port:      port,
		farBridge: SalmonTCPBridge{},
	}
	farListenAddr := fmt.Sprintf(":%d", port)
	fmt.Printf("farListenAddr: '%s' (len=%d)\n", farListenAddr, len(farListenAddr))
	far.farBridge.NewFarListen(farListenAddr)
	return far, nil
}
