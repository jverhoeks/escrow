# Go modules → Escrow Quickstart

Routes all Go module downloads through the escrow proxy, which enforces a 7-day
age gate server-side on every module fetch from the Go module mirror.

> **Age enforcement**: escrow adds server-side blocking. The standard GOPROXY has
> no age gate.

> **`,off` vs `,direct`**: always use `,off` as the fallback. `,direct` silently
> fetches from VCS origins, bypassing escrow entirely. `,off` makes the build fail
> loudly if escrow is unreachable — a safer failure mode.

---

## 1. Global setup

```bash
go env -w GOPROXY=http://localhost:8888/go,off
go env -w GONOSUMCHECK=localhost
go env -w GOFLAGS=-mod=mod
```

`GONOSUMCHECK=localhost` stops the Go checksum database from rejecting the proxy's
responses. Verify:

```bash
go env GOPROXY
# → http://localhost:8888/go,off
```

---

## 2. Per-project setup

Go does not load per-project env files automatically, but you can use a `.env`
convention with `direnv` or set variables in your CI pipeline:

```bash
# .envrc (direnv) or CI environment
export GOPROXY=http://localhost:8888/go,off
export GONOSUMCHECK=localhost
```

Alternatively, point `GOENV` at a project-level file:

```bash
# create project-level goenv file
echo 'GOPROXY=http://localhost:8888/go,off' > .goenv
echo 'GONOSUMCHECK=localhost' >> .goenv
export GOENV=$PWD/.goenv
```

---

## 3. Verify it works

```bash
go get golang.org/x/text@latest 2>&1
```

Open the dashboard:

```
http://localhost:8888/dashboard
```

Module fetches appear in the log. Modules published less than 7 days ago show a
**Blocked** badge with an **Approve** button.

---

## 4. Remove escrow

```bash
go env -u GOPROXY
go env -u GONOSUMCHECK
```

This restores the default `GOPROXY=https://proxy.golang.org,direct`.

---

## 5. Troubleshooting

**`GOPROXY list is exhausted`** — escrow is not running and `,off` prevented
fallback. Start the proxy and retry.

**`verifying ... checksum mismatch`** — set `GONOSUMCHECK=localhost` so the
checksum database is not consulted for proxy-served modules.

**Module found via `,direct` instead of escrow** — you may have another
`GOPROXY` set in your shell profile that overrides the `go env -w` setting.
Run `go env GOPROXY` to confirm the active value.
