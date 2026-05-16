#!/usr/bin/env bash
# Smoke-tests the escrow binary: /healthz and /metrics must return 200
set -euo pipefail

SENTINEL_BIN="${1:-./escrow}"
PORT=18899

cat > /tmp/escrow-smoke.toml <<EOF
[server]
  port = $PORT
[storage]
  backend = "memory"
EOF

"$SENTINEL_BIN" /tmp/escrow-smoke.toml &
PID=$!
trap "kill $PID 2>/dev/null; rm -f /tmp/escrow-smoke.toml" EXIT

for i in $(seq 1 20); do
    nc -z localhost $PORT 2>/dev/null && break
    sleep 0.2
done

CODE=$(curl -so /dev/null -w "%{http_code}" http://localhost:$PORT/healthz)
[ "$CODE" = "200" ] || { echo "FAIL: /healthz returned $CODE"; exit 1; }
echo "PASS: /healthz"

CODE=$(curl -so /dev/null -w "%{http_code}" http://localhost:$PORT/metrics)
[ "$CODE" = "200" ] || { echo "FAIL: /metrics returned $CODE"; exit 1; }
echo "PASS: /metrics"

echo "All smoke tests passed."
