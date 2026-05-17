# 🚀 Yarn Berry → Escrow Quickstart

Routes all Yarn Berry (>= 4.10) installs through the escrow proxy, which enforces
a 7-day age gate server-side on every package fetch.

> **Age enforcement**: escrow adds server-side blocking for all npm-ecosystem packages.

---

## 1. 🌐 Global setup

Yarn Berry does not have a single user-wide config file the same way npm does.
Set the registry globally with:

```bash
yarn config set npmRegistryServer http://localhost:7888
```

This writes to the global Yarn config (usually `~/.yarnrc.yml`). Verify:

```bash
yarn config get npmRegistryServer
# → http://localhost:7888
```

---

## 2. 📁 Per-project setup

Create `.yarnrc.yml` in your project root:

```yaml
npmRegistryServer: "http://localhost:7888"

# Optional: disable postinstall scripts
enableScripts: false
```

Commit `.yarnrc.yml` so all team members automatically use escrow.

---

## 3. ✅ Verify it works

```bash
yarn install --dry-run 2>&1 | head -5
```

Open the dashboard:

```
http://localhost:7888/dashboard
```

Any package fetch appears in the log. Packages younger than 7 days show a
**Blocked** badge with an **Approve** button.

---

## 4. 🗑️ Remove escrow

**Global:**
```bash
yarn config unset npmRegistryServer
```

**Per-project:** edit `.yarnrc.yml`:
```yaml
npmRegistryServer: "https://registry.npmjs.org"
```
or delete the key entirely to fall back to the default.

---

## 5. 🔧 Troubleshooting

**`YN0001: Couldn't connect to proxy`** — escrow is not running. Start it first.

**Scoped package goes to public registry** — Yarn Berry uses `npmScopes` for
per-scope overrides. Check `.yarnrc.yml` for existing `npmScopes` entries that
point elsewhere and add escrow there too:
```yaml
npmScopes:
  myorg:
    npmRegistryServer: "http://localhost:7888"
```

**`enableScripts: false` breaks a build** — a package relies on a postinstall
hook. Open the dashboard, find the package, approve it, then add it to a
`dependenciesMeta` allow-list in `package.json` if you need persistent approval.
