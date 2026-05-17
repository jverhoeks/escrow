# pip → Escrow Quickstart

Routes all pip installs through the escrow proxy's PEP 503 simple index, which
enforces a 7-day age gate server-side on every PyPI package fetch.

> **Age enforcement**: escrow adds server-side blocking. pip itself has no age gate.

---

## 1. Global setup

Edit (or create) `~/.config/pip/pip.conf` (Linux/macOS) or
`%APPDATA%\pip\pip.ini` (Windows):

```ini
[global]
index-url = http://localhost:8888/pypi/simple/
trusted-host = localhost
```

`trusted-host` is required because the proxy serves plain HTTP by default.

Verify:
```bash
pip config list
# → global.index-url='http://localhost:8888/pypi/simple/'
```

You can also set it via environment variable for one-off use:
```bash
PIP_INDEX_URL=http://localhost:8888/pypi/simple/ pip install requests
```

---

## 2. Per-project setup

Create `pip.conf` in your project root (or `pip.ini` on Windows):

```ini
[global]
index-url = http://localhost:8888/pypi/simple/
trusted-host = localhost
```

Point pip at it:
```bash
PIP_CONFIG_FILE=pip.conf pip install -r requirements.txt
```

Or set `PIP_CONFIG_FILE=pip.conf` in your `.env` / shell profile for the project.

---

## 3. Verify it works

```bash
pip install --dry-run requests 2>&1 | head -10
```

Open the dashboard:

```
http://localhost:8888/dashboard
```

Packages younger than 7 days show a **Blocked** badge with an **Approve** button.

---

## 4. Remove escrow

**Global:** delete `~/.config/pip/pip.conf` or reset the value:
```ini
[global]
index-url = https://pypi.org/simple/
```

**Per-project:** delete `pip.conf` or unset `PIP_CONFIG_FILE`.

---

## 5. Troubleshooting

**`WARNING: Retrying ... Could not find a version that satisfies`** — escrow is
not running, or the package name is wrong. Check `http://localhost:8888/dashboard`.

**SSL / certificate error** — add `trusted-host = localhost` to pip.conf; pip
treats HTTP hosts as untrusted unless explicitly listed.

**Extra index (--extra-index-url) bypasses escrow** — packages found on the extra
index skip age-gate enforcement. Remove `--extra-index-url` flags and route all
traffic through escrow.
