# The Salmon Cannon

<img src="https://salmon-cannon.s3.eu-west-2.amazonaws.com/sc_logo_small.png" alt="Salmon Cannon" width="213"/>

Salmon Cannon (`sc`) is a Linux TCP proxy for high-bandwidth links with delay,
loss, reordering, duplication, and corruption. A near node exposes SOCKS5 and
optional HTTP CONNECT listeners. Each accepted TCP connection is carried as a
reliable, ordered stream over the custom Anadromous UDP transport to a far node,
which opens the requested destination TCP connection.

```text
client -- TCP/SOCKS5 or HTTP CONNECT --> near sc
       -- reliable multiplexed UDP --> far sc -- TCP --> destination
```

Salmon Cannon is aimed at provisioned links whose capacity and MTU are known.
Anadromous deliberately has no congestion control or path-MTU discovery: the
operator must set a safe pacing rate, datagram size, and flow-control window for
the path.

## Current capabilities

- SOCKS5 TCP `CONNECT` proxying.
- Minimal HTTP `CONNECT` proxying.
- Multiple independently configured near/far bridges.
- Reliable, ordered, multiplexed streams over UDP with ACK/NACK recovery,
  adaptive retransmission timers, and per-stream flow control.
- Optional one- or two-dimensional XOR forward error correction (FEC).
- One aggregate bridge-wide rate limit and carrier-aware outbound wire pacer.
- Linux batching and offload paths: `sendmmsg`/`recvmmsg`, UDP GSO/GRO, and a
  best-effort io_uring send path with automatic fallbacks.
- Optional source, destination, and far-peer IP allowlists.
- An unauthenticated status API with optional server-side HTTPS.
- Local clean-link and hostile-netem throughput regression scripts.

> **Security warning:** Anadromous is not QUIC and provides no TLS, peer
> authentication, or cryptographic integrity. SOCKS, HTTP CONNECT, and the API
> also have no usable authentication. `SBSharedSecret` adds unauthenticated
> AES-256-CTR confidentiality to target metadata and proxied TCP bytes, but it
> is not TLS or AEAD and does not authenticate either endpoint. Run Salmon
> Cannon only on trusted/private paths, restrict listeners with host firewalls,
> or place it inside an authenticated secure tunnel.

## Requirements and build

- Linux. The Anadromous transport uses Linux-specific UDP and socket APIs.
- Go 1.25 or newer, matching `go.mod`.
- A sibling Anadromous checkout. The current development `go.mod` contains
  `replace github.com/sad-emu/anadromous => ../anadromous`, so the directory
  layout must currently be:

```text
workspace/
├── anadromous/
└── salmon-cannon/
```

Build the proxy and optional rate-test tool from the Salmon Cannon repository:

```bash
go build -o sc .
go build -o salmon-rate ./ratetest
```

`./build-sc.sh` is a short wrapper around the first command.

## Quick start

The executable always reads `./scconfig.yml` from its current working
directory; there is currently no config-path command-line flag. Put a different
file in the working directory of each endpoint.

On the far node, create `scconfig.yml`:

```yaml
SalmonBridges:
  - SBName: "wan-a"
    SBConnect: false
    SBNearPort: 55001

    # Optional exact numeric source-IP filter. Use the address observed by the
    # far node (for example, the near node's post-NAT address), or omit it.
    SBFarIp: "198.51.100.10"
```

On the near node, create `scconfig.yml`:

```yaml
SalmonBridges:
  - SBName: "wan-a"
    SBConnect: true
    SBSocksListenAddress: "127.0.0.1"
    SBSocksListenPort: 1080
    SBFarIp: "203.0.113.20"
    SBFarPort: 55001
    SBStatusCheckFrequency: 2s
```

Allow UDP port 55001 to reach the far node, start the far process first, then
the near process:

```bash
cd /path/to/far-config
/path/to/sc

cd /path/to/near-config
/path/to/sc
```

Point a SOCKS5 client at the near listener. `--socks5-hostname` keeps DNS
resolution on the far side:

```bash
curl --socks5-hostname 127.0.0.1:1080 https://example.com/
```

Both endpoints must use matching datagram-size, FEC, and shared-secret settings.

## Maximum-throughput starting point

The following is an aggressive starting point for a clean 10 Gbit/s
IPv4/Ethernet leased line with up to roughly 100 ms RTT and a verified 9,000-byte
end-to-end MTU. It is not a guarantee of 10 Gbit/s application goodput. Validate
the path MTU, host/NIC capacity, qdisc counters, and CPU placement on the actual
hosts.

At 10 Gbit/s and 100 ms RTT, the bandwidth-delay product is about 125 MB. The
256 MB in-flight cap supplies recovery margin, while the 512 MB receive ring
bounds flow control. These values are per active stream and can consume large
amounts of memory with many concurrent streams.

Near configuration:

```yaml
SalmonBridges:
  - SBName: "leased-line-10g"
    SBConnect: true
    SBSocksListenAddress: "127.0.0.1"
    SBSocksListenPort: 1080
    SBFarIp: "203.0.113.20"
    SBFarPort: 55001

    SBIdleTimeout: 1m
    SBInitialPacketSize: 8570
    SBMaxRecieveBufferSize: 512MB
    SBMaxBytesInFlight: 256MB
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms

    # G means decimal gigabits/second for SizeString fields.
    SBTotalBandwidthLimit: 10G
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBTransportBatchSize: 128

    # Avoid parity overhead on a clean path.
    SBFECGroupSize: 0
    SBFEC2D: false

# The legacy name remains part of the current YAML schema. Multiple transport
# connections help concurrent TCP flows; one TCP flow is not striped across
# the pool.
QuicConfig:
  MaxConnectionsPerBridge: 8
  MaxStreamsPerConnection: 500
  IdleCleanupTimeout: 5m
```

Far configuration:

```yaml
SalmonBridges:
  - SBName: "leased-line-10g"
    SBConnect: false
    SBNearPort: 55001
    SBFarIp: "198.51.100.10"

    SBIdleTimeout: 1m
    SBInitialPacketSize: 8570
    SBMaxRecieveBufferSize: 512MB
    SBMaxBytesInFlight: 256MB
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms

    SBTotalBandwidthLimit: 10G
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBTransportBatchSize: 128
    SBFECGroupSize: 0
    SBFEC2D: false
```

`8570` is a full UDP datagram size chosen for the tested jumbo-frame path. If
any hop cannot carry it without fragmentation, lower `SBInitialPacketSize` on
both endpoints. Smaller datagrams increase packet rate and CPU cost.

`SBTotalBandwidthLimit` is both the shared proxied-payload limiter and the
bridge-wide outbound wire-pacing budget at each endpoint. FEC and
retransmissions spend from the wire budget, so lossy-path application goodput
will be below the configured rate. Do not set the rate above the real
bottleneck: this transport intentionally does not back off.

## Poor-network regression profile

`netem_test.sh` builds isolated near/far processes under `/tmp/sc-test`, applies
netem only to UDP on `lo` by default, runs a receiver-confirmed 10-second
CRC32C-verified transfer, and fails if receiver goodput is below 2,000 Mbit/s.

Its current transport defaults are:

- 6 Gbit/s pacing budget.
- 8,570-byte datagrams.
- 512 MB per-stream receive ring and 256 MB in-flight cap.
- Batch size 128.
- FEC group 16 with 2D parity, costing 12.5% parity before retransmissions.

The netem impairment profile is intentionally unchanged:

- 50 ms delay with 10 ms normal jitter in each direction (about 100 ms RTT).
- 10% loss with 10% correlation.
- 10% reordering with 10% correlation.
- 5% duplication and 5% corruption.
- A 10,000-packet netem queue, large enough not to add accidental BDP overflow
  loss to the requested impairment profile.

Recent single-host loopback regression runs on the development machine reached
approximately 3.94-4.02 Gbit/s of receiver-confirmed payload goodput under this
profile. This is regression evidence for that machine, not a multi-host or
leased-line performance guarantee.

Run it only on a disposable test interface or namespace:

```bash
./netem_test.sh
```

The script requires `tc` and root/`CAP_NET_ADMIN` (using `sudo` when needed).
It replaces the selected interface's root qdisc and deletes its test qdisc on
exit. It saves the old qdisc as text for inspection but does **not** recreate
it, so do not point `NETIF` at an interface with production qdisc state.

Useful overrides include:

```bash
NETIF=lo TEST_BANDWIDTH=6G TEST_PACKET_SIZE=8570 \
  TEST_RECEIVE_BUFFER=512MB TEST_IN_FLIGHT=256MB \
  TEST_BATCH_SIZE=128 TEST_FEC_GROUP_SIZE=16 \
  MIN_RECEIVER_MBIT=2000 ./netem_test.sh
```

`fulltest.sh` is the corresponding clean-loopback end-to-end test. It uses a
shared secret by default, so it measures a different profile from the
unencrypted clean-path maximum-throughput example above.

## Bridge configuration

YAML field names are case-sensitive. Unknown fields are silently ignored, so
spelling matters. In particular, `SBMaxRecieveBufferSize` retains the schema's
historical `Recieve` misspelling.

### Roles, listeners, and routing

| Field | Meaning | Default |
| --- | --- | --- |
| `SBName` | Bridge identifier used by logging, status, and redirects. | Empty |
| `SBConnect` | `true` selects near/dial mode; `false` selects far/listen mode. | `false` |
| `SBSocksListenAddress` | Near-only bind address shared by the SOCKS and HTTP CONNECT listeners. | `127.0.0.1` |
| `SBSocksListenPort` | Near-only SOCKS5 TCP listener port. | `0` |
| `SBHttpListenPort` | Near-only minimal HTTP CONNECT listener; `0` disables it. | `0` |
| `SBFarPort` | Near-only remote UDP port to dial. | Copies `SBNearPort` if omitted |
| `SBNearPort` | Despite its name, the UDP listen port used by the far node. | Copies `SBFarPort` if omitted |
| `SBFarIp` | Near: remote IP/hostname to dial. Far: optional exact numeric source-IP filter for incoming transport connections. | Empty |
| `SBStatusCheckFrequency` | Near-only health-check interval; unset/`0` disables checks. | Disabled |
| `SBIdleTimeout` | How long a silent transport peer is tolerated before dead-peer cleanup. | `60s` |

The far-side `SBFarIp` comparison is an exact comparison against the observed
numeric source IP; it is not hostname, CIDR, or cryptographic authentication.

### Reliability and throughput

| Field | Meaning | Default |
| --- | --- | --- |
| `SBInitialPacketSize` | Maximum full UDP datagram size in bytes. Both endpoints must match; there is no PMTU discovery. | `1350` |
| `SBInitialRetransmitTimeout` | Initial retransmission timeout before RTT estimation converges. | `300ms` transport default |
| `SBMinRetransmitTimeout` | Floor for the adaptive retransmission timeout. | `150ms` transport default |
| `SBMaxRecieveBufferSize` | Per-stream receive/reorder ring and flow-control window, allocated per active stream. This is not the kernel UDP socket buffer. | `400MB` |
| `SBMaxBytesInFlight` | Per-stream cap on unique sent-but-unacknowledged bytes, also bounded by the stream ring. Size near BDP plus recovery margin. | Transport-derived |
| `SBTotalBandwidthLimit` | Aggregate bridge payload limit and shared outbound wire-pacing rate. `0`/unset disables both. | Unlimited/unpaced |
| `SBFECGroupSize` | Data frames protected by each XOR parity frame (`2`-`32`); `0` disables FEC. Both endpoints must match. | `8` (12.5% parity) |
| `SBFEC2D` | Adds a second orthogonal parity dimension. Total parity is `2 / group size`; both endpoints should match. | `false` |
| `SBPacingDatagramOverhead` | Bytes outside each UDP payload charged to the carrier budget. `66` models physical IPv4 Ethernet including preamble and inter-frame gap; add VLAN/encapsulation overhead if applicable. | `0` |
| `SBPacingMinimumDatagramSize` | Minimum carrier charge including overhead and padding; `84` models physical Ethernet. | `0` |
| `SBPacingBurstSize` | Optional paced token/send-batch quantum. `0` uses roughly two milliseconds of configured rate. | `0` |
| `SBTransportBatchSize` | `sendmmsg`/`recvmmsg` message count. | `64` transport default |
| `SBInterfaceName` | Bind UDP sockets with Linux `SO_BINDTODEVICE`; requires root or `CAP_NET_RAW`. | Unbound |

When `SBMaxBytesInFlight` is omitted, paced WAN connections use the stream
buffer as their effective upper bound; short-RTT and unpaced connections derive
a protective cap from the UDP receive buffer actually granted by Linux. An
explicit value replaces that dynamic policy, so an oversized value can amplify
loss into a retransmission storm.

### Access and payload confidentiality

| Field | Meaning | Default |
| --- | --- | --- |
| `SBAllowedInAddresses` | Near-only exact numeric source-IP allowlist for SOCKS clients. Empty allows all. | Empty |
| `SBAllowedOutAddresses` | Far-only exact allowlist matched against the requested hostname or IP string. Empty allows all. | Empty |
| `SBSharedSecret` | Matching pre-shared string on both endpoints for AES-256-CTR payload confidentiality. | Disabled |

`SBSharedSecret` causes each stream to generate random AES keys and IVs. Those
keys and the requested target are wrapped using a PBKDF2-SHA512 key derived from
the secret and a random salt; proxied bytes then use AES-256-CTR. There is no
MAC/AEAD tag, no peer identity, and no transport TLS. UDP framing and other
transport metadata remain visible. Use a strong random secret, but do not treat
this setting as a substitute for an authenticated tunnel.

### Value syntax

All size suffixes are uppercase, accept whole numbers only, and must not contain
spaces:

- `K`, `M`, and `G` are decimal bit quantities. For example,
  `SBTotalBandwidthLimit: 10G` is 10,000,000,000 bits/s.
- `KB`, `MB`, and `GB` are 1024-based byte quantities. For example,
  `SBMaxRecieveBufferSize: 512MB` is 512 MiB.
- A bare integer is bytes.

Durations accept a bare integer in seconds or strings ending in `ms`, `s`, or
`m`, such as `300ms`, `10s`, or `5m`.

## Transport connection pool

`QuicConfig` is the legacy YAML section name retained for compatibility. It now
controls the Anadromous connection pool; Salmon Cannon does not use QUIC.

```yaml
QuicConfig:
  MaxConnectionsPerBridge: 8
  MaxStreamsPerConnection: 500
  IdleCleanupTimeout: 5m
```

- `MaxConnectionsPerBridge` is the number of transport connections the near
  pool may create for a bridge.
- `MaxStreamsPerConnection` limits the number of active proxy streams selected
  on one pooled connection.
- `IdleCleanupTimeout` is parsed but is not currently acted on by the pool.

The pool opens a new transport connection for each new proxy stream until it
reaches `MaxConnectionsPerBridge`, then chooses the least-loaded connection.
Consequently, a pool can spread concurrent TCP flows, but one TCP flow remains
one Anadromous stream and is never striped across connections.

If the entire section is omitted, the current loader uses one connection and
500 streams per connection. A present but partial section currently has
different fallback values, so set both active numeric fields explicitly.

## Logging

Omit the entire `GlobalLog` section to log to standard output. If the section is
present but `Filename` is empty or omitted, the loader selects `sc.log`.

```yaml
GlobalLog:
  Filename: "sc.log"
  MaxSize: 20
  MaxBackups: 5
  MaxAge: 28
  Compress: false
```

`MaxSize` is MiB per file and `MaxAge` is days. Rotation is provided by
Lumberjack.

## SOCKS redirect listener

A global SOCKS listener can select a configured near bridge from a substring of
the requested hostname:

```yaml
SocksRedirect:
  Hostname: "127.0.0.1"
  Port: 8082
  Redirects:
    "example.com": "bridge-one"
    "example.org": "bridge-two"
```

Matching is case-sensitive `strings.Contains` matching. Go map iteration order
is not stable, so overlapping patterns can select nondeterministically and
should be avoided. Every target name must identify a configured `SBConnect:
true` bridge.

## Status API

The API starts only when `ApiConfig` is present:

```yaml
ApiConfig:
  Hostname: "127.0.0.1"
  Port: 8081
  TLSCert: "/path/to/server.crt"
  TLSKey: "/path/to/server.key"
```

Both `TLSCert` and `TLSKey` are required to enable HTTPS; otherwise the server
uses HTTP. The only supported requests are:

- `GET /api/v1/bridges` — configured bridge names and IDs.
- `GET /api/v1/status` — bridge limiter, transfer, stream, liveness, and ping
  counters. Liveness and ping require `SBStatusCheckFrequency` on the near node.

The API has no authentication. Bind it to a trusted address and firewall it;
HTTPS encrypts the API connection but does not add client authentication or
secure the UDP bridge.

## Rate-test tool

Build `salmon-rate` as shown above. It also reads `./scconfig.yml` from its
working directory.

```bash
# Destination listener; binds :5555 by default.
./salmon-rate -mode=listen -lport=5555

# Ten-second receiver-confirmed test through each near bridge in the config.
./salmon-rate -mode=test -cport=5555 -min-mbps=2000

# Request/response latency mode.
./salmon-rate -mode=pingpong -cport=5555
```

`-min-mbps=0` disables the throughput pass floor. Test mode processes configured
near bridges sequentially.

## Host and path tuning

Anadromous requests 16 MiB Linux UDP send and receive socket buffers and accepts
kernel clamping without emitting the old quic-go “failed to sufficiently
increase receive buffer size” error. Check both endpoints:

```bash
sysctl net.core.rmem_max net.core.wmem_max
```

If those limits are below the transport request, raise them to a value suitable
for the host. For example, 32 MiB provides room for the current request:

```bash
sudo sysctl -w net.core.rmem_max=33554432
sudo sysctl -w net.core.wmem_max=33554432
```

Persist values using the operating system's normal sysctl configuration only
after validating memory use. `SBMaxRecieveBufferSize` is a separate per-stream
application buffer and does not change these kernel settings.

For high throughput, also verify:

- End-to-end MTU with no IP fragmentation.
- NIC UDP checksum, GSO, and GRO capabilities and error counters.
- CPU saturation, IRQ/RPS/RFS placement, and NUMA locality.
- qdisc drops/overlimits and the actual carrier rate.
- A pacing rate at or below the real bottleneck.

## Known limitations

- No SOCKS5 `BIND` or `UDP ASSOCIATE` support.
- The HTTP listener supports `CONNECT` only, not ordinary forward-proxy HTTP
  requests.
- SOCKS username/password validation is not implemented. The current stub
  accepts arbitrary credentials and logs them; do not send or rely on proxy
  credentials.
- No bridge TLS, authenticated encryption, peer authentication, congestion
  control, or path-MTU discovery.
- No API authentication.
- `SalmonBounces` configuration and relay code exist, but the main executable
  does not currently start configured bounce instances.
- `QuicConfig.IdleCleanupTimeout` is not currently used for pool cleanup.

## License

[GPLv3](LICENCE)
