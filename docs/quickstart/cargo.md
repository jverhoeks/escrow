# 🚀 Cargo → Escrow Quickstart

Routes all Cargo crate downloads through the escrow proxy using the sparse
registry protocol, which enforces a 7-day age gate server-side on every crate fetch.

> **Age enforcement**: escrow adds server-side blocking. Cargo itself has no age gate.

---

## 1. 🌐 Global setup

Edit (or create) `~/.cargo/config.toml`:

```toml
[source.crates-io]
replace-with = "escrow"

[source.escrow]
registry = "sparse+http://localhost:7888/cargo/"
```

The `sparse+` prefix tells Cargo to use the HTTP sparse index protocol (requires
Cargo >= 1.68 / Rust >= 1.68).

Verify:
```bash
cargo search serde 2>&1 | head -5
```

---

## 2. 📁 Per-project setup

Create `.cargo/config.toml` in your project root (Cargo merges this with the
global config, with the project file taking precedence):

```toml
[source.crates-io]
replace-with = "escrow"

[source.escrow]
registry = "sparse+http://localhost:7888/cargo/"
```

Commit `.cargo/config.toml` so team members use escrow automatically.

---

## 3. ✅ Verify it works

```bash
cargo fetch 2>&1 | head -10
```

Open the dashboard:

```
http://localhost:7888/dashboard
```

Crates younger than 7 days show a **Blocked** badge with an **Approve** button.

---

## 4. 🗑️ Remove escrow

**Global** (`~/.cargo/config.toml`): delete or comment out the `[source.crates-io]`
and `[source.escrow]` blocks.

**Per-project:** delete `.cargo/config.toml` or remove the two source sections.

---

## 5. 🔧 Troubleshooting

**`error: failed to get `...` as a dependency`** — escrow is not running. Start
the proxy and retry `cargo build`.

**`warning: spurious network error`** — a transient failure fetching the sparse
index. Run `cargo clean` and retry. If persistent, check proxy logs.

**Old dense index cached** — if you previously used a dense index for this source,
run `cargo update` to refresh. Cargo's index cache lives in
`~/.cargo/registry/index/`; you can delete the escrow entry there to force a
full refresh.
