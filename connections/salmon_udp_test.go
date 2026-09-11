//go:build linux

package connections

import (
	"slices"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/tredeske/u/unet"
)

// Exercise the actual batched UDP syscalls on the target architecture. The
// old dependency compiled for ARM64 but used amd64 syscall numbers at runtime.
func TestBatchedUDPSyscalls(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "normal"
		if raw {
			name = "raw"
		}
		t.Run(name, func(t *testing.T) {
			newSocket := func() int {
				t.Helper()
				fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { syscall.Close(fd) })
				return fd
			}
			receiver, sender := newSocket(), newSocket()
			if err := syscall.Bind(receiver, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}}); err != nil {
				t.Fatal(err)
			}
			addr, err := syscall.Getsockname(receiver)
			if err != nil {
				t.Fatal(err)
			}
			if err := syscall.Connect(sender, addr); err != nil {
				t.Fatal(err)
			}

			want := []string{"first UDP datagram", "second UDP datagram"}
			var sendEP, recvEP unet.UdpEndpoint
			i := 0
			sendEP.SetupVectors(len(want), 1, func(iov []syscall.Iovec) {
				payload := []byte(want[i])
				iov[0].Base = &payload[0]
				iov[0].Len = uint64(len(payload))
				i++
			}, nil)
			recvBuffers := make([][]byte, len(want))
			i = 0
			recvEP.SetupVectors(len(want), 1, func(iov []syscall.Iovec) {
				recvBuffers[i] = make([]byte, 64)
				iov[0].Base = &recvBuffers[i][0]
				iov[0].Len = uint64(len(recvBuffers[i]))
				i++
			}, nil)
			send, receive := unet.SendMMsg, unet.RecvMMsg
			if raw {
				send, receive = unet.RawSendMMsg, unet.RawRecvMMsg
			}
			n, errno := send(uintptr(sender), uintptr(unsafe.Pointer(&sendEP.Hdrs[0])), uintptr(len(sendEP.Hdrs)))
			if errno != 0 || n != len(want) {
				t.Fatalf("sendmmsg sent %d datagrams, error %v", n, errno)
			}
			var got []string
			deadline := time.Now().Add(2 * time.Second)
			for len(got) < len(want) && time.Now().Before(deadline) {
				n, errno = receive(uintptr(receiver), uintptr(unsafe.Pointer(&recvEP.Hdrs[0])), uintptr(len(recvEP.Hdrs)))
				if errno == syscall.EAGAIN || errno == syscall.EINTR {
					time.Sleep(time.Millisecond)
					continue
				}
				if errno != 0 {
					t.Fatalf("recvmmsg: %v", errno)
				}
				for i := 0; i < n; i++ {
					got = append(got, string(recvBuffers[i][:recvEP.Hdrs[i].NTransferred]))
				}
			}
			if !slices.Equal(got, want) {
				t.Fatalf("received %q, want %q", got, want)
			}
		})
	}
}
