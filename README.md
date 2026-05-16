# escrow

A lightweight Go proxy for npm and PyPI that enforces a minimum release age,
scans for known CVEs, and scores packages for trust signals — blocking or
warning based on operator policy.

## Quick start

```bash
docker run -p 8888:8888 ghcr.io/jverhoeks/escrow:latest
```

Point npm at escrow:

```ini
# .npmrc
registry=http://localhost:8888/
```

Point pip/uv at escrow:

```toml
# uv.toml
[pip]
index-url = "http://localhost:8888/pypi/simple/"
```

## Configuration

Copy `config.example.toml` to `escrow.toml` and run `./escrow escrow.toml`.

Without a `[policy]` section, escrow proxies transparently and prints a
startup warning. Add `[policy]` sections to enable age gate, OSV scanning,
and trust signals.

## Building

```bash
go build -o escrow ./cmd/escrow
./escrow config.example.toml
```

## Testing

```bash
go test ./...
bash scripts/smoke-test.sh ./escrow
```

## Policy reference

See `config.example.toml` for the full configuration reference with comments.
