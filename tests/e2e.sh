#!/usr/bin/env bash
# E2E integration tests: builds escrow and exercises npm, PyPI, and Go proxy
# against live upstream registries.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="/tmp/escrow-e2e-$$"
ALLOW_CFG="/tmp/escrow-allow-$$.toml"
BLOCK_CFG="/tmp/escrow-block-$$.toml"
PORT=18989

PASS=0; FAIL=0
ESCROW_PID=""

cleanup() {
    kill "$ESCROW_PID" 2>/dev/null || true
    rm -f "$BIN" "$ALLOW_CFG" "$BLOCK_CFG"
}
trap cleanup EXIT

ok()  { echo "PASS: $1"; PASS=$((PASS+1)); }
fail(){ echo "FAIL: $1"; FAIL=$((FAIL+1)); }

# Build
echo "Building escrow..."
cd "$REPO_ROOT"
go build -o "$BIN" ./cmd/escrow

# --- Allow-all config (no policy section) ---
cat > "$ALLOW_CFG" <<EOF
[server]
  host = "127.0.0.1"
  port = $PORT

[storage]
  backend = "memory"

[ecosystems]
  npm  = true
  pypi = true
  go   = true

[dashboard]
  enabled = false
EOF

echo "Starting escrow (allow-all)..."
"$BIN" "$ALLOW_CFG" &
ESCROW_PID=$!
for i in $(seq 1 20); do
    [ -n "$ESCROW_PID" ] && kill -0 "$ESCROW_PID" 2>/dev/null || { echo "ERROR: escrow exited early"; exit 1; }
    curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done

# healthz
if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null; then ok "healthz"; else fail "healthz"; fi

# npm manifest pass-through
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('name')=='lodash' else 1)"; then
    ok "npm manifest pass-through"
else
    fail "npm manifest pass-through"
fi

# npm has versions (allow-all)
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if len(d.get('versions',{})) > 0 else 1)"; then
    ok "npm versions present (allow-all)"
else
    fail "npm versions present (allow-all)"
fi

# PyPI simple index
if curl -sf "http://127.0.0.1:$PORT/pypi/simple/requests/" | grep -qi "requests"; then
    ok "pypi simple index pass-through"
else
    fail "pypi simple index pass-through"
fi

# PyPI JSON API
if curl -sf "http://127.0.0.1:$PORT/pypi/requests/json" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if len(d.get('releases',{})) > 0 else 1)"; then
    ok "pypi json api pass-through"
else
    fail "pypi json api pass-through"
fi

# Go list
if curl -sf "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/list" | grep -qE '^v0\.[0-9]+'; then
    ok "go list pass-through"
else
    fail "go list pass-through"
fi

# Go info (old version, should pass)
if curl -sf "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/v0.3.0.info" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('Version')=='v0.3.0' else 1)"; then
    ok "go info pass-through"
else
    fail "go info pass-through"
fi

kill "$ESCROW_PID" 2>/dev/null
wait "$ESCROW_PID" 2>/dev/null || true
for i in $(seq 1 10); do nc -z 127.0.0.1 $PORT 2>/dev/null || break; sleep 0.2; done
ESCROW_PID=""

# --- Block-all config (age gate = 99999 days) ---
cat > "$BLOCK_CFG" <<EOF
[server]
  host = "127.0.0.1"
  port = $PORT

[storage]
  backend = "memory"

[ecosystems]
  npm  = true
  pypi = true
  go   = true

[policy.age]
  min_days = 99999
  action   = "block"

[dashboard]
  enabled = false
EOF

echo "Starting escrow (block-all)..."
"$BIN" "$BLOCK_CFG" &
ESCROW_PID=$!
for i in $(seq 1 20); do
    [ -n "$ESCROW_PID" ] && kill -0 "$ESCROW_PID" 2>/dev/null || { echo "ERROR: escrow exited early"; exit 1; }
    curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
    sleep 0.3
done

# npm: all versions blocked → empty versions map
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if len(d.get('versions',{})) == 0 else 1)"; then
    ok "npm all versions blocked"
else
    fail "npm all versions blocked"
fi

# npm: no latest dist-tag when all blocked
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if 'latest' not in d.get('dist-tags',{}) else 1)"; then
    ok "npm no latest dist-tag when blocked"
else
    fail "npm no latest dist-tag when blocked"
fi

# PyPI: almost all releases blocked → far fewer than allow-all (99999-day gate blocks all
# except a handful of very early versions whose upload_time fails to parse and falls back to
# the zero time, which reads as ~735 000 days old and thus passes the gate)
PYPI_BLOCK_COUNT=$(curl -sf "http://127.0.0.1:$PORT/pypi/requests/json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('releases',{})))")
if [ "$PYPI_BLOCK_COUNT" -lt 10 ]; then
    ok "pypi almost all releases blocked ($PYPI_BLOCK_COUNT remaining)"
else
    fail "pypi almost all releases blocked (expected < 10, got $PYPI_BLOCK_COUNT)"
fi

# Go info: 403 when blocked
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/v0.3.0.info")
if [ "$HTTP_CODE" = "403" ]; then
    ok "go info blocked (403)"
else
    fail "go info blocked (expected 403, got $HTTP_CODE)"
fi

echo ""
echo "Results: $PASS passed, $FAIL failed"
[ $FAIL -eq 0 ]
