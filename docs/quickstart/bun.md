# 🚀 Bun → Escrow Quickstart

Routes all Bun (>= 1.3) package installs through the escrow proxy, which enforces
a 7-day age gate server-side on every npm package fetch.

> **Age enforcement**: escrow adds server-side blocking. Bun has no built-in age gate.

---

## 1. 🌐 Global setup

Edit (or create) `~/.bunfig.toml`:

```toml
[install]
registry = "http://localhost:7888"
```

Verify:
```bash
bun pm ls 2>&1 || bun install --dry-run 2>&1 | head -3
```

---

## 2. 📁 Per-project setup

Create `bunfig.toml` in your project root:

```toml
[install]
registry = "http://localhost:7888"
```

Per-project `bunfig.toml` overrides the global config. Commit it so team members
use escrow automatically.

For scoped packages, add per-scope overrides:

```toml
[install.scopes]
"@myorg" = { registry = "http://localhost:7888" }
```

---

## 3. ✅ Verify it works

```bash
bun install --dry-run 2>&1 | head -5
```

Then open the dashboard:

```
http://localhost:7888/dashboard
```

Any fetch that hit the proxy appears there. Packages younger than 7 days show a
**Blocked** badge with an **Approve** button to manually allow through.

---

## 4. 🗑️ Remove escrow

**Global** (`~/.bunfig.toml`): delete the `registry` line or set it back:
```toml
[install]
registry = "https://registry.npmjs.org"
```

**Per-project:** delete `bunfig.toml` or replace the registry line.

---

## 5. 🔧 Troubleshooting

**`error: Failed to fetch registry metadata`** — escrow is not running. Start
the proxy before running `bun install`.

**Scoped package uses wrong registry** — Bun reads `[install.scopes]` from
`bunfig.toml`. Check if a nested or parent `bunfig.toml` defines a conflicting
scope entry.

**`bun install` ignores `bunfig.toml`** — Bun only loads `bunfig.toml` from the
directory where you run the command. Run `bun install` from the project root where
the file lives, not from a subdirectory.
