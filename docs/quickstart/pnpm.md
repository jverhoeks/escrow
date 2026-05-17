# 🚀 pnpm → Escrow Quickstart

Routes all pnpm installs through the escrow proxy, which enforces a 7-day age gate
server-side and blocks packages published less than a week ago.

> **Age enforcement**: escrow adds server-side blocking. pnpm has a client-side
> `minimumReleaseAge` feature but it requires per-project config; escrow enforces it
> for every project automatically.

> **Config split (v11)**: pnpm v11 no longer reads `allowBuilds`/`strictDepBuilds`
> from `.npmrc` — those go in `pnpm-workspace.yaml`. The registry URL still lives in
> `.npmrc`.

---

## 1. 🌐 Global setup

```bash
pnpm config set registry http://localhost:7888
```

Writes to the global `.npmrc` (usually `~/.config/pnpm/rc` or `~/.npmrc`). Verify:

```bash
pnpm config get registry
# → http://localhost:7888
```

---

## 2. 📁 Per-project setup

### pnpm v10 and v11 — `.npmrc` in project root

```ini
registry=http://localhost:7888
```

### pnpm v11 only — `pnpm-workspace.yaml` (build script control)

```yaml
onlyBuiltDependencies: []   # opt-in allowlist; empty = block all postinstall scripts
```

Commit both files so team members use escrow automatically.

---

## 3. ✅ Verify it works

```bash
pnpm install --dry-run 2>&1 | head -5
```

Open the dashboard to confirm the request appeared:

```
http://localhost:7888/dashboard
```

Packages younger than 7 days show a **Blocked** badge with an **Approve** button.

---

## 4. 🗑️ Remove escrow

**Global:**
```bash
pnpm config delete registry
```

**Per-project `.npmrc`:** delete the file or replace the line:
```ini
registry=https://registry.npmjs.org
```

**`pnpm-workspace.yaml`:** remove the `onlyBuiltDependencies` key or delete the file.

---

## 5. 🔧 Troubleshooting

**`ERR_PNPM_META_FETCH_FAIL`** — escrow is not running. Start the proxy first.

**v11 postinstall scripts not blocked** — move `onlyBuiltDependencies` from `.npmrc`
to `pnpm-workspace.yaml`; v11 ignores build-policy keys in `.npmrc`.

**`minimumReleaseAge` not triggering** — this is a client-side pnpm setting using
minutes (e.g. `minimumReleaseAge=10080` = 7 days). With escrow, you can omit it
because the proxy enforces age server-side for all clients.
