package main

import (
	"context"
	"io"
	"net"
)

// TCPBridge implements the Bridge interface for direct TCP/UDP forwarding.
type TCPBridge struct{}

// ForwardTCP forwards a TCP connection to the destination address.
func (b *TCPBridge) ForwardTCP(ctx context.Context, src net.Conn, destAddr string) error {
	dst, err := net.Dial("tcp", destAddr)
	if err != nil {
		return err
	}
	defer dst.Close()

	done := make(chan struct{})
	// Copy src -> dst
	go func() {
		io.Copy(dst, src)
		done <- struct{}{}
	}()
	// Copy dst -> src
	go func() {
		io.Copy(src, dst)
		done <- struct{}{}
	}()

	// Wait for either direction to finish or context to cancel
	select {
	case <-done:
	case <-ctx.Done():
	}
	return nil
}

// ForwardUDP forwards a UDP packet to the destination address.
func (b *TCPBridge) ForwardUDP(ctx context.Context, packet []byte, destAddr string) error {
	conn, err := net.Dial("udp", destAddr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write(packet)
	return err
}
