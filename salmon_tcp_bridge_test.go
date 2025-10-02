package main

import (
	"context"
	"net"
	"testing"
	"time"
)

// func TestSalmonTcpBridge_ConnectAndListen(t *testing.T) {
// 	listener, err := net.Listen("tcp", "127.0.0.1:0")
// 	if err != nil {
// 		t.Fatalf("failed to listen: %v", err)
// 	}
// 	defer listener.Close()

// 	bridgeServer := &SalmonTcpBridge{}
// 	go func() {
// 		err := bridgeServer.Listen(listener, func(data []byte) ([]byte, error) {
// 			pkt, err := DeserializeSalmonTCPPacket(data)
// 			if err != nil {
// 				return nil, err
// 			}
// 			respPkt := SalmonTCPPacket{
// 				RemoteAddr: pkt.RemoteAddr,
// 				remotePort: pkt.remotePort,
// 				Data:       pkt.Data, // echo the data
// 			}
// 			return SerializeSalmonTCPPacket(respPkt)
// 		})
// 		if err != nil {
// 			t.Errorf("Listen error: %v", err)
// 		}
// 	}()

// 	bridgeClient := &SalmonTcpBridge{}
// 	err = bridgeClient.Connect(listener.Addr().String())
// 	if err != nil {
// 		t.Fatalf("Connect failed: %v", err)
// 	}
// 	defer bridgeClient.Close()

// 	pkt := SalmonTCPPacket{RemoteAddr: "echo", remotePort: 0, Data: []byte("hello")} // remoteAddr/port ignored by echo
// 	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
// 	defer cancel()
// 	resp, err := bridgeClient.ForwardTCP(ctx, pkt)
// 	if err != nil {
// 		t.Fatalf("ForwardTCP failed: %v", err)
// 	}
// 	respPkt, err := DeserializeSalmonTCPPacket(resp)
// 	if err != nil {
// 		t.Fatalf("DeserializeSalmonTCPPacket failed: %v", err)
// 	}
// 	if string(respPkt.Data) != "hello" {
// 		t.Errorf("expected echo, got %q", string(respPkt.Data))
// 	}
// }

func TestSalmonTcpBridge_HttpRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()

	bridgeServer := &SalmonTcpBridge{}
	go func() {
		bridgeServer.Listen(listener)

	}()

	bridgeClient := &SalmonTcpBridge{}
	err = bridgeClient.Connect(listener.Addr().String())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	defer bridgeClient.Close()

	pkt := SalmonTCPPacket{
		RemoteAddr: "146.190.62.39",
		remotePort: 80,
		Data:       []byte("GET / HTTP/1.0\r\nHost: httpforever.com\r\n\r\n"),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := bridgeClient.ForwardTCP(ctx, pkt)
	print("UNITTEST - Response length: ")
	println(len(resp))
	if err != nil {
		t.Fatalf("ForwardTCP failed: %v", err)
	}
	if len(resp) == 0 || string(resp[:4]) != "HTTP" {
		t.Errorf("expected HTTP response, got %q", string(resp))
	}
}
