# Local UDP portability fix

This directory contains the production Go sources of the `unet`, `uerr`,
`uexit`, `uio`, and `ulog` packages from
`github.com/sad-emu/u@v0.0.0-20250421110454-ea06120e3caa`. These are the packages
used transitively by Anadromous. The upstream MIT license is retained in
`LICENSE`; unrelated packages and upstream tests are omitted, and `go.mod`
lists only the dependencies needed by this subset.

The sole source change is in `unet/unet.go`: `SYS_RECVMMSG` and `SYS_SENDMMSG`
use `golang.org/x/sys/unix`'s architecture-specific constants instead of the
amd64-only numbers 299 and 307. On Linux ARM64 these syscalls are 243 and 269.
The wrong receive syscall causes Anadromous's established-connection receive
loop to exit, even though its listener's `recvfrom` handshake can succeed.

The root `go.mod` replaces `github.com/tredeske/u` with this directory so the
fix applies to ordinary builds and cross-compilation. Remove this replacement
when the upstream dependency includes the fix. The loopback batched-UDP
regression test is in `connections/salmon_udp_test.go`, where the main test
suite runs it on both native and emulated ARM64 builds.
