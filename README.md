# The Salmon Cannon
<img src="https://salmon-cannon.s3.eu-west-2.amazonaws.com/sc_logo_small.png" alt="SalmonCannon" width="213"/>

## NOT READY FOR USE
SOCKS5 auth & bridge authentication is TODO. Do not use this project yet.
## Description
SalmonCannon (sc) is a SOCKS5 proxy that tunnels TCP traffic between a 'near' node and a 'far' node using the QUIC protocol. It is designed for efficient, and reliable TCP forwarding in challenging network environments.

[client(s)]--tcp-->[Near sc]--QUIC-->[Far sc]--TCP-->[server(s)]

## Features
- **SOCKS5 Proxy:** Accepts TCP connections from SOCKS5 clients.
- **QUIC Tunneling:** Transports TCP streams over QUIC between near and far nodes.
- **Configurable:** Flexible YAML configuration for multiple bridges and advanced options.
- **TCP:** Supports TCP through a SOCKS5 interface
- **HTTP:** Can proxy HTTP traffic directly
- **Optional IP Filtering:** Can filter near clients, destination connections and bridge connections via IPs & Hostnames

## TODO's
- **UDP:** UDP support through the SOCKS5 interface is TODO
- **SOCKS5 & HTTP Auth:** Support SOCKS5 and HTTP proxy authentication is TODO
- **Bridge TLS:** QUIC TLS is currently hardcoded to use self-signed certs. Allowing own certs with 2way TLS & DN filtering is TODO

## Architecture
- **Near Node:** Listens for SOCKS5 connections and forwards them to the far node over QUIC.
- **Far Node:** Accepts QUIC connections and proxies TCP traffic to the destination.

## Quick Start

To run:
1. Ensure your UDP buffers are configred on the host (see common errors below)
2. Place the `scconfig.yml` file in the same directory as the `sc` binary.
3. Run `./sc`.

### 1. Minimal Example

#### Logging
If no logging config is provided the sc binary with log to stdout.
```yaml
GlobalLog:
  Filename: "sc.log"
  MaxSize: 20        # megabytes
  MaxBackups: 5
  MaxAge: 28         # days
  Compress: false

```


#### Near Node (Connect Mode)
```yaml
SalmonBridges:
  - SBName: "salmon-bridge-1-minimal"
    SBSocksListenPort: 1080
    SBConnect: true
    SBNearPort: 55001
    SBFarPort: 55001
    SBFarIp: "far-ip-here"
```

#### Far Node (Accept Mode)
```yaml
SalmonBridges:
  - SBName: "salmon-bridge-1-minimal"
    SBConnect: false
    SBNearPort: 55001
    SBFarPort: 55001
```

### 2. Full Example

#### Near Node (Connect Mode)
```yaml
SalmonBridges:
  - SBName: "salmon-bridge-1-full"
    SBSocksListenPort: 1080
    SBHttpListenPort: 8080
    SBConnect: true
    SBStatusCheckFrequency: 2s
    SBFarPort: 55001
    SBFarIp: "far-ip-here"
    SBSocksListenAddress: "127.0.0.1"
    SBIdleTimeout: 1m
    SBInitialPacketSize: 1350
    SBTotalBandwidthLimit: 100M
    SBInterfaceName: "eth0"
    SBMaxRecieveBufferSize: 1GB
    SBAllowedInAddresses:
      - "127.0.0.1"
      - "127.0.0.2"
```

#### Far Node (Accept Mode)
```yaml
SalmonBridges:
  - SBName: "salmon-bridge-1-full"
    SBFarIp: "near-ip-here"
    SBConnect: false
    SBNearPort: 55002
    SBIdleTimeout: 1m
    SBInitialPacketSize: 1350
    SBTotalBandwidthLimit: 100M
    SBInterfaceName: "eth0"
    SBMaxRecieveBufferSize: 1GB
    SBAllowedOutAddresses:
      - "example.com"
      - "example2.com"
```

### 3. Maximum Throughput Example (10 Gbit/s)

This is an aggressive starting point for a symmetric 10 Gbit/s IPv4/Ethernet
leased line with no intentional packet loss, an RTT of up to roughly 100 ms,
and an end-to-end 9,000-byte jumbo MTU. Both endpoints must use the same packet
and FEC settings. If any hop cannot carry jumbo frames, lower
`SBInitialPacketSize` to fit the path MTU; this increases packet rate and CPU
cost.

The 256 MB receive and in-flight limits are per active stream. Size them from
the bandwidth-delay product (`bytes/second × RTT`) plus recovery margin, and
reduce them when supporting many simultaneous streams.

#### Near Node (Connect Mode)

```yaml
SalmonBridges:
  - SBName: "leased-line-10g"
    SBConnect: true
    SBSocksListenAddress: "127.0.0.1"
    SBSocksListenPort: 1080
    SBFarIp: "far-ip-here"
    SBFarPort: 55001
    SBInterfaceName: "eth0"

    SBIdleTimeout: 1m
    SBInitialPacketSize: 8500
    SBMaxRecieveBufferSize: 256MB
    SBMaxBytesInFlight: 256MB
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms

    # `G` is gigabits/second. This is one aggregate wire budget shared by
    # every transport connection belonging to the bridge.
    SBTotalBandwidthLimit: 10G
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBTransportBatchSize: 64

    # Avoid parity overhead on a clean leased line.
    SBFECGroupSize: 0
    SBFEC2D: false

QuicConfig:
  MaxConnectionsPerBridge: 8
  MaxStreamsPerConnection: 500
  IdleCleanupTimeout: 5m
```

#### Far Node (Accept Mode)

```yaml
SalmonBridges:
  - SBName: "leased-line-10g"
    SBConnect: false
    SBFarIp: "near-ip-here"
    SBNearPort: 55001
    SBInterfaceName: "eth0"

    SBIdleTimeout: 1m
    SBInitialPacketSize: 8500
    SBMaxRecieveBufferSize: 256MB
    SBMaxBytesInFlight: 256MB
    SBInitialRetransmitTimeout: 300ms
    SBMinRetransmitTimeout: 150ms

    SBTotalBandwidthLimit: 10G
    SBPacingDatagramOverhead: 66
    SBPacingMinimumDatagramSize: 84
    SBTransportBatchSize: 64
    SBFECGroupSize: 0
    SBFEC2D: false

QuicConfig:
  MaxConnectionsPerBridge: 8
  MaxStreamsPerConnection: 500
  IdleCleanupTimeout: 5m
```

For a lossy path, set `SBFECGroupSize: 8` and `SBFEC2D: true` on both
endpoints. This spends 25% of the bulk wire budget on parity before any
retransmissions, so application goodput will be lower than the configured
line rate. Leave `SBSharedSecret` unset for the highest throughput, configure
the host UDP buffers described under [Common Issues](#common-issues), and
confirm throughput using NIC/qdisc counters rather than application rate
alone.

## Usage
1. **Configure** your near and far nodes using YAML as shown above.
2. **Start the near node** in connect mode to initiate QUIC connections to the far node.
3. **Start the far node** in accept mode to receive QUIC connections and proxy TCP traffic.
4. **Point your SOCKS5 client** (e.g., browser, curl, proxychains) to the near node's listen address and port. curl --socks5-hostname 127.0.0.1:1080 https://www.google.com/

## Bridges Configuration Reference
- `SBName`: Bridge name (string)
- `SBSocksListenPort`: SOCKS5 listen port (int)
- `SBSocksListenAddress`: SOCKS5 listen address (string, optional)
- `SBHttpListenPort`: HTTP proxy listen port on near node (int, optional; 0 disables)
- `SBConnect`: If true, acts as near node (initiates QUIC connection)
- `SBStatusCheckFrequency`: Frequency of status checks for bridge health monitoring (duration e.g. 200ms or 5s, optional)
- `SBNearPort`: QUIC port on near node - Far ONLY (int)
- `SBFarPort`: QUIC port on far node - Near ONLY (int)
- `SBFarIp`: Far node IP address for the near, acts as a IP/Hostname filter if set on the far
- `SBIdleTimeout`: Idle timeout (duration e.g. 10s or 2m, optional)
- `SBInitialRetransmitTimeout`: Retransmission timeout used before RTT estimation converges (duration, optional; protocol default 300ms).
- `SBMinRetransmitTimeout`: Lower bound for the adaptive retransmission timeout (duration, optional; protocol default 150ms).
- `SBInitialPacketSize`: QUIC initial packet size (int e.g. 50M, optional)
- `SBTotalBandwidthLimit`: Aggregate bridge bandwidth limit (size in bits e.g. 100M or 1G, optional). It limits proxied payload and supplies the shared outbound wire-pacing rate.
- `SBPacingDatagramOverhead`: Extra carrier-counted bytes charged for every UDP datagram. Use `66` for physical IPv4 Ethernet including preamble/inter-frame gap, `28` for IPv4 packet accounting, or `0` for UDP-payload accounting. Add VLAN/carrier encapsulation bytes as required.
- `SBPacingMinimumDatagramSize`: Minimum carrier-counted size after overhead/padding. Physical Ethernet including preamble/inter-frame gap normally uses `84`; `0` disables a minimum.
- `SBPacingBurstSize`: Optional target paced send-batch/token quantum in bytes (e.g. `512KB`). Omit to use approximately 2ms of the configured rate; one indivisible datagram may exceed the target.
- `SBTransportBatchSize`: Optional sendmmsg/recvmmsg message count. Omit to use Anadromous's default of 64; paced sends also flush when their wire-byte quantum is reached.
- `SBMaxRecieveBufferSize`: Max buffer for incomming packets (size in bytes e.g. 500 MB or 1GB, optional)
- `SBMaxBytesInFlight`: Per-stream cap on sent but unacknowledged bytes (e.g. 32MB). Omit or set to 0 to derive it from the UDP receive buffer.
- `SBFECGroupSize`: Number of DATA frames protected by each XOR parity frame (2-32). Omit to use Anadromous's default of 8; set to 0 to disable FEC. Must match on both ends.
- `SBFEC2D`: Add a second, orthogonal parity dimension for high-loss links. Requires FEC and should match on both ends; adds another `1 / SBFECGroupSize` of wire overhead.
- `SBInterfaceName`: Network interface you wish to attach through. (Optional)
- `SBAllowedInAddresses`: Near node only. List of hostname/IPs allowed to connect to the near. (Allows all if not set)
- `SBAllowedOutAddresses`: Far node only. List of hostname/IPs connections can be proxies to. (Allows all if not set)
- `SBSharedSecret`: Allows bridges to be encrypted with a pre shared secret. Will reduce performance. Entirely optional, QUIC already enforces TLS.

### Logging Configuration (`GlobalLog`)
Logging is configured via the `GlobalLog` section in your config:

```yaml
GlobalLog:
  Filename: "sc.log"   # Log file name
  MaxSize: 20          # Max log file size (megabytes)
  MaxBackups: 5        # Max number of old log files to keep
  MaxAge: 28           # Max number of days to retain old log files
  Compress: false      # Whether to compress old log files
```

- `Filename`: Log file name (string). If not set will output to stdout
- `MaxSize`: Maximum log file size before rotation (int, megabytes)
- `MaxBackups`: Maximum number of backup log files to keep (int)
- `MaxAge`: Maximum number of days to retain old log files (int, days)
- `Compress`: Whether to compress rotated log files (bool)

### SOCKS Redirect Configuration (`SocksRedirect`)
The `SocksRedirect` section in your config allows you to use a single 'generic' SOCKS listener to route to specific bridges based on the desired endpoint. The requested IP/Hostname will use the first key that is a partial match, so be careful!

```yaml
SocksRedirect:
  Hostname: "localhost"
  Port: 8082
  Redirects:
    "example.com": "bridge-one"
    "example.org": "bridge-two"
```

### API Configuration (`ApiConfig`)
The API server is configured via the `ApiConfig` section in your config:

```yaml
ApiConfig:
  Hostname: "localhost"
  Port: 8081
  TLSCert: "/path/to/server.crt"  # Optional: Path to TLS certificate file
  TLSKey: "/path/to/server.key"   # Optional: Path to TLS key file
```

- `Hostname`: Hostname for the server
- `Port`: Port for the server
- `TLSCert`: (Optional) Path to TLS certificate file for HTTPS
- `TLSKey`: (Optional) Path to TLS key file for HTTPS

**API TLS/HTTPS Support:**
- If both `TLSCert` and `TLSKey` are provided the API server will use HTTPS
- If either is omitted the server defaults to HTTP

#### Supported Requests

- `/api/v1/bridges` - JSON List of loaded bridges
- `/api/v1/status` - JSON List of bridge status including bandwidth usage, alive status, and ping metrics. Alive and ping metrics requires SBStatusCheckFrequency to be set on the NEAR bridge.

### QUIC Configuration (`QuicConfig`)
The `QuicConfig` section controls QUIC connection pooling behavior. This allows for performance tuning if the bottleneck becomes the QUIC connection.

```yaml
QuicConfig:
  MaxConnectionsPerBridge: 1
  MaxStreamsPerConnection: 500
  IdleCleanupTimeout: 5m
```

- `MaxConnectionsPerBridge`: Maximum number of QUIC connections in the pool per bridge (int, default: 1).
- `MaxStreamsPerConnection`: Maximum concurrent streams per QUIC connection (int, default: 500).
- `IdleCleanupTimeout`: Duration after which idle QUIC connections are removed from the pool (duration e.g. 5m or 10m, default: 5m).

**Connection Pooling Behavior:**
- When a new TCP stream needs to be proxied, the bridge will create a new connection until the `MaxConnectionsPerBridge` is reached.
- It will then use the bridge with the fewest streams
- Old connections will be cleaned up when not in use

## Crypto Info
### TLS
TLS is built into the QUIC protocol. Currently - a 2048bit RSA key is generated for each Far bridge on startup.

### Bridge Config - (`SBSharedSecret`)
The bridges can be configured with a pre shared secret. It is currently implemented as AES256-CTR. The encryption key is derived using a combination of the `SBSharedSecret` and the `BridgeName`.

This will:
- Encrypt the traffic passing over the bridge (on-top of the encryption TLS already provides)
- Reduce performance (approx 20%)


## Ratetest App

Built with the 'build-ratetest.sh' command. It requires a valid scconfig.yml file to configure the tests.

### Modes
#### Listen
./salmon-rate -mode=listen

Listens on port 5555 for incomming TCP connections on 127.0.0.1
#### Test
./salmon-rate -mode=test

Uses the config to start a 10 sec ratetest on all of the salmonbridges configured with 'connect: true'.

## Common Issues
### UDP Init Error  
failed to sufficiently increase receive buffer size 
(was: 208 kiB, wanted: 7168 kiB, got: 416 kiB). 
See https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes for details.

#### Fix

Add these lines to /etc/sysctl.conf:

Code
net.core.wmem_max=838860800
net.core.wmem_default=83886080
net.core.rmem_max=838860800
net.core.rmem_default=83886080

Then apply with

sudo sysctl -p

## License
GPLv3
