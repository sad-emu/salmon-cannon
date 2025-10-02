package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestSalmonTcpBridge_ConnectAndListen(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeServer := &SalmonTcpBridge{}
	go func() {
		err := bridgeServer.Listen(listener, func(data []byte) ([]byte, error) {
			// Echo handler
			return data, nil
		})
		if err != nil {
			t.Errorf("Listen error: %v", err)
		}
	}()

	bridgeClient := &SalmonTcpBridge{}
	err = bridgeClient.Connect(listener.Addr().String())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer bridgeClient.Close()

	pkt := SalmonTCPPacket{RemoteAddr: "echo", remotePort: 0, Data: []byte("hello")} // remoteAddr/port ignored by echo
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	resp, err := bridgeClient.ForwardTCP(ctx, pkt)
	if err != nil {
		t.Fatalf("ForwardTCP failed: %v", err)
	}
	if string(resp) != "hello" {
		t.Errorf("expected echo, got %q", string(resp))
	}
}

func TestSalmonTcpBridge_GoogleRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeServer := &SalmonTcpBridge{}
	go func() {
		err := bridgeServer.Listen(listener, func(data []byte) ([]byte, error) {
			pkt, err := DeserializeSalmonTCPPacket(data)
			if err != nil {
				return nil, err
			}
			addr := net.JoinHostPort(pkt.RemoteAddr, "80")
			conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				return nil, err
			}
			defer conn.Close()
			_, err = conn.Write(pkt.Data)
			if err != nil {
				return nil, err
			}
			buf := make([]byte, 4096)
			n, err := conn.Read(buf)
			if err != nil && n == 0 {
				return nil, err
			}
			respPkt := SalmonTCPPacket{RemoteAddr: pkt.RemoteAddr, remotePort: pkt.remotePort, Data: buf[:n]}
			return SerializeSalmonTCPPacket(respPkt)
		})
		if err != nil {
			t.Errorf("Listen error: %v", err)
		}
	}()

	bridgeClient := &SalmonTcpBridge{}
	err = bridgeClient.Connect(listener.Addr().String())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer bridgeClient.Close()

	pkt := SalmonTCPPacket{
		RemoteAddr: "google.com",
		remotePort: 80,
		Data:       []byte("GET / HTTP/1.0\r\nHost: google.com\r\n\r\n"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := bridgeClient.ForwardTCP(ctx, pkt)
	if err != nil {
		t.Fatalf("ForwardTCP failed: %v", err)
	}
	if len(resp) == 0 || string(resp[:4]) != "HTTP" {
		t.Errorf("expected HTTP response, got %q", string(resp))
	}
}
