package main

import (
	"context"
	"net"
)

// Bridge abstracts forwarding traffic to a remote or local endpoint.
type Bridge interface {
	// ForwardTCP forwards a TCP connection to the far endpoint.
	ForwardTCP(ctx context.Context, src net.Conn, destAddr string) error

	// ForwardUDP forwards a UDP packet to the far endpoint.
	ForwardUDP(ctx context.Context, packet []byte, destAddr string) error
}
