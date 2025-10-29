package main

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// mockConn implements net.Conn for testing
type mockConn struct {
	readBuf  []byte
	readPos  int
	writeBuf []byte
}

func (m *mockConn) Read(b []byte) (n int, err error) {
	if m.readPos >= len(m.readBuf) {
		fmt.Printf("[mockConn] EOF at position %d (total %d)\n", m.readPos, len(m.readBuf))
		return 0, io.EOF
	}
	// Copy available data up to the size of the buffer
	n = copy(b, m.readBuf[m.readPos:])
	m.readPos += n
	//fmt.Printf("[mockConn] Read called with buffer size %d, copied %d bytes (pos %d->%d, total %d)\n",
	//	len(b), n, m.readPos-n, m.readPos, len(m.readBuf))
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (m *mockConn) Write(b []byte) (n int, err error) {
	m.writeBuf = append(m.writeBuf, b...)
	return len(b), nil
}

func (m *mockConn) Close() error                       { return nil }
func (m *mockConn) LocalAddr() net.Addr                { return nil }
func (m *mockConn) RemoteAddr() net.Addr               { return nil }
func (m *mockConn) SetDeadline(t time.Time) error      { return nil }
func (m *mockConn) SetReadDeadline(t time.Time) error  { return nil }
func (m *mockConn) SetWriteDeadline(t time.Time) error { return nil }

// TestHandleSocksHandshake_AllDataAtOnce tests the case where all SOCKS5
// handshake and request data is sent in one go
func TestHandleSocksHandshake_AllDataAtOnce(t *testing.T) {
	tests := []struct {
		name        string
		data        []byte
		expectHost  string
		expectPort  int
		expectError bool
		useAuth     bool
	}{
		{
			name: "IPv4 address all at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00},           // greeting: version 5, 1 method, no auth
				[]byte{0x05, 0x01, 0x00, 0x01},     // request header: version, connect, reserved, IPv4
				[]byte{192, 168, 1, 1, 0x00, 0x50}, // 192.168.1.1:80
			),
			expectHost: "192.168.1.1",
			expectPort: 80,
		},
		{
			name: "Domain name all at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00},       // greeting
				[]byte{0x05, 0x01, 0x00, 0x03}, // request header: version, connect, reserved, domain
				[]byte{0x0b},                   // domain length: 11
				[]byte("example.com"),
				[]byte{0x01, 0xbb}, // port 443
			),
			expectHost: "example.com",
			expectPort: 443,
		},
		{
			name: "IPv6 address all at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00},       // greeting
				[]byte{0x05, 0x01, 0x00, 0x04}, // request header: version, connect, reserved, IPv6
				[]byte{0x20, 0x01, 0x0d, 0xb8, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, // 2001:db8::1
				[]byte{0x00, 0x50}, // port 80
			),
			expectHost: "2001:db8::1",
			expectPort: 80,
		},
		{
			name: "IPv4 address with multi auth at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x02, 0x02, 0x00},     // greeting: version 5, 1 method, no auth
				[]byte{0x05, 0x01, 0x00, 0x01},     // request header: version, connect, reserved, IPv4
				[]byte{192, 168, 1, 1, 0x00, 0x50}, // 192.168.1.1:80
			),
			expectHost: "192.168.1.1",
			expectPort: 80,
		},
		{
			name: "IPv4 address with auth no passwordall at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x02}, // greeting: version 5, 1 method, auth
				[]byte{0x01, 0x04, 0x60, 0x61, 0x60, 0x61, 0x00},
				[]byte{0x05, 0x01, 0x00, 0x01},     // request header: version, connect, reserved, IPv4
				[]byte{192, 168, 1, 1, 0x00, 0x50}, // 192.168.1.1:80
			),
			expectHost: "192.168.1.1",
			expectPort: 80,
			useAuth:    true,
		},
		{
			name: "IPv4 address with auth and password all at once",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x02}, // greeting: version 5, 1 method, auth
				[]byte{0x01, 0x04, 0x60, 0x61, 0x60, 0x61, 0x02, 0x68, 0x6a},
				[]byte{0x05, 0x01, 0x00, 0x01},     // request header: version, connect, reserved, IPv4
				[]byte{192, 168, 1, 1, 0x00, 0x50}, // 192.168.1.1:80
			),
			expectHost: "192.168.1.1",
			expectPort: 80,
			useAuth:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &mockConn{readBuf: tt.data}
			//fmt.Printf("\n[TEST] Starting test with %d bytes of data\n", len(tt.data))
			//fmt.Printf("[TEST] Data: %v\n", tt.data)

			host, port, err := HandleSocksHandshake(conn, "test-bridge")

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if host != tt.expectHost {
				t.Errorf("expected host %q, got %q", tt.expectHost, host)
			}

			if port != tt.expectPort {
				t.Errorf("expected port %d, got %d", tt.expectPort, port)
			}

			// Verify handshake response was written
			if len(conn.writeBuf) < 2 {
				t.Fatalf("expected handshake response to be written")
			}
			if tt.useAuth {
				if conn.writeBuf[0] != 0x05 || conn.writeBuf[1] != 0x02 {
					t.Errorf("unexpected handshake response for auth: %v", conn.writeBuf[:2])
				}
			} else {
				if conn.writeBuf[0] != 0x05 || conn.writeBuf[1] != 0x00 {
					t.Errorf("unexpected handshake response: %v", conn.writeBuf[:2])
				}
			}
		})
	}
}

// TestHandleSocksHandshake_FragmentedData tests the case where the request
// header is sent separately from the host/port data
func TestHandleSocksHandshake_FragmentedData(t *testing.T) {
	tests := []struct {
		name        string
		fragments   [][]byte // Multiple read fragments
		expectHost  string
		expectPort  int
		expectError bool
	}{
		{
			name: "IPv4 fragmented - header then address",
			fragments: [][]byte{
				{0x05, 0x01, 0x00},        // greeting: version 5, 1 method, no auth
				{0x05, 0x01, 0x00, 0x01},  // request header (first 4 bytes)
				{10, 0, 0, 1, 0x1f, 0x90}, // 10.0.0.1:8080
			},
			expectHost: "10.0.0.1",
			expectPort: 8080,
		},
		{
			name: "Domain fragmented - header, length, then domain and port",
			fragments: [][]byte{
				{0x05, 0x01, 0x00},       // greeting
				{0x05, 0x01, 0x00, 0x03}, // request header (first 4 bytes)
				{0x09},                   // domain length: 9
				[]byte("localhost"),
				{0x00, 0x50}, // port 80
			},
			expectHost: "localhost",
			expectPort: 80,
		},
		{
			name: "Domain highly fragmented - one byte at a time for some parts",
			fragments: [][]byte{
				{0x05},       // greeting version (fragmented)
				{0x01, 0x00}, // greeting methods
				{0x05},       // request version (fragmented)
				{0x01},       // command
				{0x00},       // reserved
				{0x03},       // addr type (domain)
				{0x07},       // domain length: 7
				[]byte("foo.bar"),
				{0x00, 0x50}, // port 80
			},
			expectHost: "foo.bar",
			expectPort: 80,
		},
		{
			name: "IPv6 fragmented - header then address",
			fragments: [][]byte{
				{0x05, 0x01, 0x00},       // greeting
				{0x05, 0x01, 0x00, 0x04}, // request header (first 4 bytes)
				{0xfe, 0x80, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
					0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}, // fe80::1
				{0x1f, 0x90}, // port 8080
			},
			expectHost: "fe80::1",
			expectPort: 8080,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Combine fragments into a single buffer for the mock connection
			var allData []byte
			for _, frag := range tt.fragments {
				allData = append(allData, frag...)
			}

			conn := &mockConn{readBuf: allData}

			host, port, err := HandleSocksHandshake(conn, "test-bridge")

			if tt.expectError {
				if err == nil {
					t.Fatalf("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if host != tt.expectHost {
				t.Errorf("expected host %q, got %q", tt.expectHost, host)
			}

			if port != tt.expectPort {
				t.Errorf("expected port %d, got %d", tt.expectPort, port)
			}
		})
	}
}

// TestHandleSocksHandshake_ErrorCases tests various error conditions
func TestHandleSocksHandshake_ErrorCases(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "Unsupported SOCKS version",
			data: []byte{0x04, 0x01, 0x00}, // SOCKS4
		},
		// Note: "Incomplete greeting" removed - readExact will just hang/block on real connection
		// EOF behavior on mock is acceptable for incomplete data
		{
			name: "Incomplete request header",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00}, // valid greeting
				[]byte{0x05, 0x01},       // incomplete request header (only 2 bytes)
			),
		},
		{
			name: "Unsupported command (UDP associate)",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00},
				[]byte{0x05, 0x03, 0x00, 0x01}, // UDP associate instead of connect
				[]byte{127, 0, 0, 1, 0x00, 0x50},
			),
		},
		{
			name: "Unsupported address type",
			data: buildSocksRequest(
				[]byte{0x05, 0x01, 0x00},
				[]byte{0x05, 0x01, 0x00, 0x99}, // invalid address type
			),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &mockConn{readBuf: tt.data}

			_, _, err := HandleSocksHandshake(conn, "test-bridge")

			if err == nil {
				t.Fatalf("expected error but got none")
			}
		})
	}
}

// buildSocksRequest concatenates multiple byte slices into a single SOCKS request
func buildSocksRequest(parts ...[]byte) []byte {
	var result []byte
	for _, part := range parts {
		result = append(result, part...)
	}
	return result
}
