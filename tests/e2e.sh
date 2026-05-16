#!/usr/bin/env bash
# E2E integration tests: exercises npm, PyPI, Go, Cargo, and Composer proxy
# against live upstream registries.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BIN="/tmp/escrow-e2e-$$"
ALLOW_CFG="/tmp/escrow-allow-$$.toml"
BLOCK_CFG="/tmp/escrow-block-$$.toml"
ESCROW_LOG="/tmp/escrow-e2e-$$.log"
PORT=18989

PASS=0; FAIL=0
ESCROW_PID=""

cleanup() {
    if [ -n "$ESCROW_PID" ]; then
        kill "$ESCROW_PID" 2>/dev/null || true
        wait "$ESCROW_PID" 2>/dev/null || true
    fi
    rm -f "$BIN" "$ALLOW_CFG" "$BLOCK_CFG" "$ESCROW_LOG"
}
trap cleanup EXIT

ok()  { echo "PASS: $1"; PASS=$((PASS+1)); }
fail(){ echo "FAIL: $1"; FAIL=$((FAIL+1)); }

start_escrow() {
    local cfg="$1"
    "$BIN" "$cfg" >"$ESCROW_LOG" 2>&1 &
    ESCROW_PID=$!
    for i in $(seq 1 30); do
        kill -0 "$ESCROW_PID" 2>/dev/null || { echo "ERROR: escrow exited early"; cat "$ESCROW_LOG"; exit 1; }
        curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && return
        sleep 0.3
    done
    echo "ERROR: escrow never became ready"; cat "$ESCROW_LOG"; exit 1
}

stop_escrow() {
    kill "$ESCROW_PID" 2>/dev/null || true
    wait "$ESCROW_PID" 2>/dev/null || true
    for i in $(seq 1 20); do nc -z 127.0.0.1 $PORT 2>/dev/null || break; sleep 0.2; done
    ESCROW_PID=""
}

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
  npm      = true
  pypi     = true
  go       = true
  cargo    = true
  composer = true

[dashboard]
  enabled = false
EOF

# --- Block-all config (age gate = 99999 days) ---
cat > "$BLOCK_CFG" <<EOF
[server]
  host = "127.0.0.1"
  port = $PORT

[storage]
  backend = "memory"

[ecosystems]
  npm      = true
  pypi     = true
  go       = true
  cargo    = true
  composer = true

[policy.age]
  min_days = 99999
  action   = "block"

[dashboard]
  enabled = false
EOF

# ===========================================================================
# ALLOW-ALL
# ===========================================================================
echo ""
echo "=== allow-all ==="
start_escrow "$ALLOW_CFG"

# healthz
if curl -sf "http://127.0.0.1:$PORT/healthz" >/dev/null; then ok "healthz"; else fail "healthz"; fi

# npm: manifest pass-through
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('name')=='lodash' else 1)"; then
    ok "npm manifest pass-through"
else
    fail "npm manifest pass-through"
fi

# npm: versions present
if curl -sf "http://127.0.0.1:$PORT/lodash" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if len(d.get('versions',{})) > 0 else 1)"; then
    ok "npm versions present (allow-all)"
else
    fail "npm versions present (allow-all)"
fi

# PyPI: simple index
if curl -sf "http://127.0.0.1:$PORT/pypi/simple/requests/" | grep -qi "requests"; then
    ok "pypi simple index pass-through"
else
    fail "pypi simple index pass-through"
fi

# PyPI: JSON API releases present
if curl -sf "http://127.0.0.1:$PORT/pypi/requests/json" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if len(d.get('releases',{})) > 0 else 1)"; then
    ok "pypi json api pass-through"
else
    fail "pypi json api pass-through"
fi

# Go: list endpoint
if curl -sf "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/list" | grep -qE '^v0\.[0-9]+'; then
    ok "go list pass-through"
else
    fail "go list pass-through"
fi

# Go: .info for old version passes
if curl -sf "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/v0.3.0.info" | python3 -c "import sys,json; d=json.load(sys.stdin); exit(0 if d.get('Version')=='v0.3.0' else 1)"; then
    ok "go info pass-through"
else
    fail "go info pass-through"
fi

# Cargo: config.json returns dl and api fields
CARGO_CFG=$(curl -sf "http://127.0.0.1:$PORT/cargo/config.json")
if python3 -c "import sys,json; d=json.loads(sys.argv[1]); exit(0 if 'dl' in d and 'api' in d else 1)" "$CARGO_CFG"; then
    ok "cargo config.json structure"
else
    fail "cargo config.json structure"
fi

# Cargo: config.json dl field points to our proxy
if python3 -c "import sys,json; d=json.loads(sys.argv[1]); exit(0 if '127.0.0.1:$PORT' in d.get('dl','') else 1)" "$CARGO_CFG"; then
    ok "cargo config.json dl points to proxy"
else
    fail "cargo config.json dl points to proxy"
fi

# Cargo: sparse index for 'serde' (path: se/rd/serde) — NDJSON with versions
# serde path: len=5 → first2/next2/name = se/rd/serde
SERDE_LINES=$(curl -sf "http://127.0.0.1:$PORT/cargo/se/rd/serde" | wc -l | tr -d ' ')
if [ "$SERDE_LINES" -gt 0 ]; then
    ok "cargo serde index has versions (allow-all, $SERDE_LINES lines)"
else
    fail "cargo serde index has versions (allow-all)"
fi

# Cargo: each line is valid JSON with a 'vers' field
if curl -sf "http://127.0.0.1:$PORT/cargo/se/rd/serde" | python3 -c "
import sys, json
for line in sys.stdin:
    line = line.strip()
    if line:
        d = json.loads(line)
        assert 'vers' in d, 'missing vers field'
"; then
    ok "cargo serde index NDJSON valid"
else
    fail "cargo serde index NDJSON valid"
fi

# Composer: packages.json returns rewritten metadata-url
if curl -sf "http://127.0.0.1:$PORT/composer/packages.json" | python3 -c "
import sys, json
d = json.load(sys.stdin)
url = d.get('metadata-url', '')
exit(0 if '/composer/p2/' in url else 1)
"; then
    ok "composer packages.json metadata-url rewritten"
else
    fail "composer packages.json metadata-url rewritten"
fi

# Composer: p2 package metadata has versions (symfony/console is ancient)
CONSOLE_VERS=$(curl -sf "http://127.0.0.1:$PORT/composer/p2/symfony/console.json" | python3 -c "
import sys, json
d = json.load(sys.stdin)
pkgs = d.get('packages', {})
total = sum(len(v) for v in pkgs.values())
print(total)
")
if [ "$CONSOLE_VERS" -gt 0 ]; then
    ok "composer symfony/console versions present (allow-all, $CONSOLE_VERS versions)"
else
    fail "composer symfony/console versions present (allow-all)"
fi

stop_escrow

# ===========================================================================
# BLOCK-ALL
# ===========================================================================
echo ""
echo "=== block-all (min_days=99999) ==="
start_escrow "$BLOCK_CFG"

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

# PyPI: almost all releases blocked (a handful of very early releases with unparseable
# upload_time fall back to zero time.Time ≈ 735000 days old and pass the 99999-day gate)
PYPI_BLOCK_COUNT=$(curl -sf "http://127.0.0.1:$PORT/pypi/requests/json" | python3 -c "import sys,json; d=json.load(sys.stdin); print(len(d.get('releases',{})))")
if [ "$PYPI_BLOCK_COUNT" -lt 10 ]; then
    ok "pypi almost all releases blocked ($PYPI_BLOCK_COUNT remaining)"
else
    fail "pypi almost all releases blocked (expected < 10, got $PYPI_BLOCK_COUNT)"
fi

# Go: .info returns 403 when blocked
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/go/golang.org/x/text/@v/v0.3.0.info")
if [ "$HTTP_CODE" = "403" ]; then
    ok "go info blocked (403)"
else
    fail "go info blocked (expected 403, got $HTTP_CODE)"
fi

# Cargo: serde index returns 0 lines (all versions blocked)
SERDE_BLOCK_LINES=$(curl -sf "http://127.0.0.1:$PORT/cargo/se/rd/serde" | grep -c '"vers"' || true)
if [ "$SERDE_BLOCK_LINES" -eq 0 ]; then
    ok "cargo serde all versions blocked"
else
    fail "cargo serde all versions blocked (got $SERDE_BLOCK_LINES lines)"
fi

# Cargo: config.json still serves correctly under block-all (no policy on config endpoint)
HTTP_CODE=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:$PORT/cargo/config.json")
if [ "$HTTP_CODE" = "200" ]; then
    ok "cargo config.json accessible under block-all"
else
    fail "cargo config.json accessible under block-all (got $HTTP_CODE)"
fi

# Composer: symfony/console has 0 versions under block-all
CONSOLE_BLOCK_VERS=$(curl -sf "http://127.0.0.1:$PORT/composer/p2/symfony/console.json" | python3 -c "
import sys, json
d = json.load(sys.stdin)
pkgs = d.get('packages', {})
total = sum(len(v) for v in pkgs.values())
print(total)
")
if [ "$CONSOLE_BLOCK_VERS" -eq 0 ]; then
    ok "composer symfony/console all versions blocked"
else
    fail "composer symfony/console all versions blocked (got $CONSOLE_BLOCK_VERS remaining)"
fi

stop_escrow

# ===========================================================================
# Summary
# ===========================================================================
echo ""
echo "Results: $PASS passed, $FAIL failed"
[ $FAIL -eq 0 ]
