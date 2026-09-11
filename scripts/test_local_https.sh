#!/usr/bin/env bash
# Build and run two local sc processes, then verify real HTTPS through SOCKS.
# FAR_ARCH=arm64 also exercises an ARM64 far process under qemu-aarch64.
# PROXY_DNS_MODE=thread or daemon tests ProxyChains DNS instead of curl's SOCKS.
# KEEP_TEST_ARTIFACTS=1 retains configs, logs, and the response on success.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
for dependency in go curl python3; do
    if ! command -v "$dependency" >/dev/null 2>&1; then
        echo "Missing required command: $dependency" >&2
        exit 1
    fi
done

PROXY_DNS_MODE="${PROXY_DNS_MODE:-curl}"
case "$PROXY_DNS_MODE" in
    curl) ;;
    thread|daemon)
        command -v proxychains4 >/dev/null
        if [[ "$PROXY_DNS_MODE" == daemon ]]; then
            command -v proxychains4-daemon >/dev/null
            command -v getent >/dev/null
        fi
        ;;
    *) echo "PROXY_DNS_MODE must be curl, thread, or daemon" >&2; exit 1 ;;
esac

HOST_ARCH="$(go env GOHOSTARCH)"
FAR_ARCH="${FAR_ARCH:-$HOST_ARCH}"
FAR_RUNNER=()
if [[ "$FAR_ARCH" != "$HOST_ARCH" ]]; then
    if [[ "$FAR_ARCH" != arm64 ]]; then
        echo "Cross-architecture testing supports FAR_ARCH=arm64 only" >&2
        exit 1
    fi
    if ! command -v qemu-aarch64 >/dev/null 2>&1; then
        echo "FAR_ARCH=arm64 requires qemu-aarch64 on this host" >&2
        exit 1
    fi
    FAR_RUNNER=(qemu-aarch64)
fi

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/sc-https-test.XXXXXX")"
NEAR_PID=""
FAR_PID=""
DNS_PID=""
cleanup() {
    local result=$?
    trap - EXIT
    for pid in "$NEAR_PID" "$FAR_PID" "$DNS_PID"; do
        if [[ -n "$pid" ]]; then
            kill "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    if [[ "$result" -ne 0 ]]; then
        for logfile in "$WORKDIR/far.log" "$WORKDIR/near.log" "$WORKDIR/dns.log"; do
            if [[ -f "$logfile" ]]; then
                echo "Last lines of $logfile:" >&2
                tail -n 30 "$logfile" >&2
            fi
        done
        echo "FAIL: test artifacts retained at $WORKDIR" >&2
    elif [[ "${KEEP_TEST_ARTIFACTS:-0}" == 1 ]]; then
        echo "Test artifacts: $WORKDIR"
    else
        rm -rf -- "$WORKDIR"
    fi
    exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$WORKDIR/bin" "$WORKDIR/near" "$WORKDIR/far"
cd "$REPO_ROOT"
echo "Building sc for near=$HOST_ARCH, far=$FAR_ARCH"
GOOS=linux GOARCH="$HOST_ARCH" CGO_ENABLED=0 go build -o "$WORKDIR/bin/sc-near" .
if [[ "$FAR_ARCH" == "$HOST_ARCH" ]]; then
    cp "$WORKDIR/bin/sc-near" "$WORKDIR/bin/sc-far"
else
    GOOS=linux GOARCH="$FAR_ARCH" CGO_ENABLED=0 go build -o "$WORKDIR/bin/sc-far" .
fi

# Ask the OS for unused ports instead of assuming 1080 or a fixed UDP port
# is available. A bind race causes a visible startup failure, never reuse of
# another proxy: both processes must report readiness and remain alive.
read -r FAR_PORT SOCKS_PORT < <(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as udp, socket.socket() as tcp:
    udp.bind(("127.0.0.1", 0))
    tcp.bind(("127.0.0.1", 0))
    print(udp.getsockname()[1], tcp.getsockname()[1])
PY
)

cat > "$WORKDIR/far/scconfig.yml" <<EOF
SalmonBridges:
  - SBName: "local-https-far"
    SBConnect: false
    SBNearPort: $FAR_PORT
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: 60s
    SBInitialPacketSize: 1350
    SBMaxStreams: 50
    SBMaxRecieveBufferSize: 100MB
    SBTotalBandwidthLimit: 1M
EOF

cat > "$WORKDIR/near/scconfig.yml" <<EOF
SalmonBridges:
  - SBName: "local-https-near"
    SBConnect: true
    SBSocksListenAddress: "127.0.0.1"
    SBSocksListenPort: $SOCKS_PORT
    SBFarIp: "127.0.0.1"
    SBFarPort: $FAR_PORT
    SBStatusCheckFrequency: 2s
    SBIdleTimeout: 60s
    SBInitialPacketSize: 1350
    SBTotalBandwidthLimit: 1M
EOF

wait_for_ready() {
    local pid=$1 logfile=$2 marker=$3
    for ((attempt = 0; attempt < 100; attempt++)); do
        if ! kill -0 "$pid" 2>/dev/null; then
            echo "sc exited before readiness: $logfile" >&2
            return 1
        fi
        if [[ -f "$logfile" ]] && [[ "$(<"$logfile")" == *"$marker"* ]]; then
            return 0
        fi
        sleep 0.1
    done
    echo "Timed out waiting for readiness: $logfile" >&2
    return 1
}

(
    cd "$WORKDIR/far"
    exec "${FAR_RUNNER[@]}" "$WORKDIR/bin/sc-far"
) > "$WORKDIR/far.log" 2>&1 &
FAR_PID=$!
wait_for_ready "$FAR_PID" "$WORKDIR/far.log" "Bridge local-https-far listening on"

(
    cd "$WORKDIR/near"
    exec "$WORKDIR/bin/sc-near"
) > "$WORKDIR/near.log" 2>&1 &
NEAR_PID=$!
wait_for_ready "$NEAR_PID" "$WORKDIR/near.log" "SOCKS proxy listening on"

CURL_RUNNER=()
CURL_PROXY_ARGS=(--socks5-hostname "127.0.0.1:$SOCKS_PORT")
if [[ "$PROXY_DNS_MODE" != curl ]]; then
    DNS_DIRECTIVE=proxy_dns
    if [[ "$PROXY_DNS_MODE" == daemon ]]; then
        DNS_PORT="$(python3 - <<'PY'
import socket
with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)"
        DNS_DIRECTIVE="proxy_dns_daemon 127.0.0.1:$DNS_PORT"
        proxychains4-daemon -i 127.0.0.1 -p "$DNS_PORT" -r 224 > "$WORKDIR/dns.log" 2>&1 &
        DNS_PID=$!
    fi
    cat > "$WORKDIR/proxychains.conf" <<EOF
strict_chain
$DNS_DIRECTIVE
remote_dns_subnet 224
tcp_read_time_out 15000
tcp_connect_time_out 8000
localnet 127.0.0.0/255.0.0.0
localnet ::1/128
[ProxyList]
socks5 127.0.0.1 $SOCKS_PORT
EOF
    if [[ "$PROXY_DNS_MODE" == daemon ]]; then
        # The daemon has no ready log. Exercise its hostname mapping with a
        # bounded client; an absent daemon otherwise leaves ProxyChains waiting.
        python3 - "$WORKDIR/proxychains.conf" <<'PY'
import subprocess
import sys
for attempt in range(5):
    try:
        result = subprocess.run(
            ["proxychains4", "-f", sys.argv[1], "getent", "ahostsv4", "google.com"],
            capture_output=True, text=True, timeout=1,
        )
        if result.returncode == 0 and result.stdout.startswith("224."):
            break
    except subprocess.TimeoutExpired:
        pass
else:
    sys.exit("ProxyChains DNS daemon did not become ready")
PY
        kill -0 "$DNS_PID"
    fi
    CURL_RUNNER=(proxychains4 -f "$WORKDIR/proxychains.conf")
    # ProxyChains intercepts curl's connections; disable curl's own proxy.
    CURL_PROXY_ARGS=(--proxy '')
fi

echo "Requesting https://google.com/ through local SOCKS port $SOCKS_PORT and UDP port $FAR_PORT"
# -q disables .curlrc overrides (including insecure mode). Empty --noproxy
# prevents NO_PROXY from bypassing SOCKS. TLS verification stays enabled;
# redirects are followed only over HTTPS and every hop uses the local proxy.
"${CURL_RUNNER[@]}" curl -q --silent --show-error --fail --location --max-redirs 5 \
    --proto '=https' --proto-redir '=https' --noproxy '' \
    "${CURL_PROXY_ARGS[@]}" \
    --connect-timeout 10 --max-time 30 \
    --dump-header "$WORKDIR/headers.txt" --output "$WORKDIR/response.html" \
    --write-out '%{http_code} %{ssl_verify_result} %{size_download} %{url_effective}\n' \
    'https://google.com/' > "$WORKDIR/result.txt"

read -r HTTP_CODE TLS_RESULT RESPONSE_BYTES FINAL_URL < "$WORKDIR/result.txt"
if [[ "$HTTP_CODE" != 200 || "$TLS_RESULT" != 0 || ! -s "$WORKDIR/response.html" ]]; then
    echo "Unexpected result: HTTP $HTTP_CODE, TLS verification $TLS_RESULT, bytes $RESPONSE_BYTES" >&2
    exit 1
fi
if [[ "$(<"$WORKDIR/far.log")" != *"connected to TCP target google.com:443"* ]]; then
    echo "The far process did not confirm a TCP connection to google.com:443" >&2
    exit 1
fi
kill -0 "$NEAR_PID" "$FAR_PID"
echo "PASS: HTTP $HTTP_CODE from $FINAL_URL; TLS certificate verified; $RESPONSE_BYTES bytes through near=$HOST_ARCH and far=$FAR_ARCH; DNS=$PROXY_DNS_MODE"
