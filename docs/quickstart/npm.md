# 🚀 npm → Escrow Quickstart

Routes all npm installs through the escrow proxy, which enforces a 7-day age gate
server-side and blocks packages published less than a week ago.

> **Age enforcement**: escrow adds server-side blocking. npm itself has no built-in age gate.
> **Also recommended**: set `ignore-scripts=true` — escrow blocks on age/vulns but does not
> strip postinstall hooks from packages that do pass.

---

## 1. 🌐 Global setup (all projects on this machine)

```bash
npm config set registry http://localhost:8888
npm config set ignore-scripts true
```

This writes to `~/.npmrc`. Verify:

```bash
npm config get registry
# → http://localhost:8888
```

---

## 2. 📁 Per-project setup

Create `.npmrc` in your project root:

```ini
registry=http://localhost:8888
ignore-scripts=true
```

Per-project `.npmrc` takes precedence over the global file. Commit it so every
developer on the project uses escrow automatically.

---

## 3. ✅ Verify it works

```bash
npm install --dry-run 2>&1 | head -5
```

Then open the dashboard:

```
http://localhost:8888/dashboard
```

Any request that hit the proxy appears there. Packages younger than 7 days show
a red **Blocked** badge with an **Approve** button you can click to allow through.

---

## 4. 🗑️ Remove escrow

**Global:**
```bash
npm config delete registry
npm config delete ignore-scripts
```

**Per-project:** delete `.npmrc` or replace the registry line:
```ini
registry=https://registry.npmjs.org
```

---

## 5. 🔧 Troubleshooting

**`npm ERR! code ETIMEDOUT`** — escrow is not running. Start it with `./escrow` or
check `docker compose up -d` if you use the Docker image.

**Package blocked but you need it now** — open `http://localhost:8888/dashboard`,
find the package, and click **Approve**. The approval is cached; re-run `npm install`.

**Scripts still run despite `ignore-scripts=true`** — check that no nested `.npmrc`
or `--ignore-scripts=false` CLI flag is overriding the config.
`npm config list` shows the effective config stack.
