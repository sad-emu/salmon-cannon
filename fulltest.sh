#!/usr/bin/env bash
# run_ratetest.sh — updated: starts a ratetest listener on the far side (mode=listen)
#
# Usage: run from repository root (where main.go and ratetest/ live).
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORKDIR="/tmp/sc-test"
FAR_DIR="$WORKDIR/far"
NEAR_DIR="$WORKDIR/near"
BIN_DIR="$WORKDIR/bin"

FAR_PORT=8444
SOCKS_PORT=1080

SC_BIN="$BIN_DIR/sc"
RATETEST_BIN="$BIN_DIR/salmon-rate"

cleanup() {
    echo "Cleaning up..."
    if [[ -n "${FAR_PID:-}" ]]; then
        echo "Killing far (pid $FAR_PID)"
        kill "$FAR_PID" 2>/dev/null || true
    fi
    if [[ -n "${NEAR_PID:-}" ]]; then
        echo "Killing near (pid $NEAR_PID)"
        kill "$NEAR_PID" 2>/dev/null || true
    fi
    if [[ -n "${RATELISTEN_PID:-}" ]]; then
        echo "Killing ratetest listener (pid $RATELISTEN_PID)"
        kill "$RATELISTEN_PID" 2>/dev/null || true
    fi
    sleep 0.3
}
trap cleanup EXIT

echo "==> Preparing workspace: $WORKDIR"
rm -rf "$WORKDIR"
mkdir -p "$FAR_DIR" "$NEAR_DIR" "$BIN_DIR"

if ! command -v go >/dev/null 2>&1; then
    echo "Go toolchain not found in PATH. Please install Go and retry."
    exit 1
fi

echo "==> Building binaries into $BIN_DIR"
cd "$REPO_ROOT"
go build -o "$SC_BIN" ./ || { echo "go build sc failed"; exit 1; }
go build -o "$RATETEST_BIN" ./ratetest || { echo "go build ratetest failed"; exit 1; }

echo "==> Writing configuration files"
cat > "$FAR_DIR/scconfig.yml" <<EOF
SalmonBridges:
  - SBName: "sc-near"
    SBConnect: false
    SBNearPort: ${FAR_PORT}
    SBSocksListenPort: 0
    SBSocksListenAddress: "127.0.0.1"
    SBIdleTimeout: 60s
    SBInitialPacketSize: 1400
    SBMaxRecieveBufferSize: 2GB
    SBInterfaceName: "lo"
GlobalLog:
  Filename: "sc.log"
  MaxSize: 5
  MaxBackups: 2
  MaxAge: 1
  Compress: false
EOF

cat > "$NEAR_DIR/scconfig.yml" <<EOF
SalmonBridges:
  - SBName: "sc-near"
    SBSocksListenPort: ${SOCKS_PORT}
    SBSocksListenAddress: "127.0.0.1"
    SBHttpListenPort: 0
    SBConnect: true
    SBFarPort: ${FAR_PORT}
    SBFarIp: "127.0.0.1"
    SBIdleTimeout: 60s
    SBInitialPacketSize: 1400
    SBMaxRecieveBufferSize: 2GB
    SBInterfaceName: "lo"
GlobalLog:
  Filename: "sc.log"
  MaxSize: 5
  MaxBackups: 2
  MaxAge: 1
  Compress: false
EOF

echo "==> Starting far instance (accept mode) in background"
cd "$FAR_DIR"
nohup "$SC_BIN" > far.stdout.log 2> far.stderr.log &
FAR_PID=$!
echo "Far PID: $FAR_PID"
sleep 0.8
sleep 0.6

echo "==> Starting ratetest listener on far (mode=listen) in background"
cd "$FAR_DIR"
nohup "$RATETEST_BIN" -mode=listen > ratetest-listen.stdout.log 2> ratetest-listen.stderr.log &
RATELISTEN_PID=$!
echo "ratetest listener PID: $RATELISTEN_PID"
sleep 0.6

echo "==> Starting near instance (connect mode) in background"
cd "$NEAR_DIR"
nohup "$SC_BIN" > near.stdout.log 2> near.stderr.log &
NEAR_PID=$!
echo "Near PID: $NEAR_PID"
sleep 1
sleep 1
sleep 1
echo "==> Running ratetest (mode=test) in near directory, output will be shown below"
cd "$NEAR_DIR"
set +e
"$RATETEST_BIN" -mode=test 2>&1 | tee ratetest_full_output.txt
RT_EXIT=$?
set -e

if [[ $RT_EXIT -ne 0 ]]; then
    echo "ratetest exited with code $RT_EXIT"
else
    echo "ratetest completed successfully."
fi

echo
echo "Far logs: $FAR_DIR/sc.log  (stdout: $FAR_DIR/far.stdout.log)"
echo "Near logs: $NEAR_DIR/sc.log (stdout: $NEAR_DIR/near.stdout.log)"
echo "ratetest listener logs: $FAR_DIR/ratetest-listen.stdout.log (stderr: $FAR_DIR/ratetest-listen.stderr.log)"
echo
echo
echo "===== ratetest summary (extracted) ====="
tail -n 40 ratetest_full_output.txt || true
echo "========================================"

# exit -> cleanup trap