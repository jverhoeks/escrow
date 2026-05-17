# 🚀 uv → Escrow Quickstart

Routes all uv package installs through the escrow proxy's PEP 503 simple index,
which enforces a 7-day age gate server-side on every PyPI package fetch.

> **Age enforcement**: escrow adds server-side blocking. uv itself has no age gate.

---

## 1. 🌐 Global setup

Edit (or create) `~/.config/uv/uv.toml`:

```toml
[pip]
index-url = "http://localhost:7888/pypi/simple/"

[[index]]
url = "http://localhost:7888/pypi/simple/"
default = true
```

Verify:
```bash
uv pip install --dry-run requests 2>&1 | head -5
```

---

## 2. 📁 Per-project setup

Create `uv.toml` in your project root:

```toml
[pip]
index-url = "http://localhost:7888/pypi/simple/"

[[index]]
url = "http://localhost:7888/pypi/simple/"
default = true
```

uv also reads `tool.uv` from `pyproject.toml`:

```toml
[tool.uv]
index-url = "http://localhost:7888/pypi/simple/"

[[tool.uv.index]]
url = "http://localhost:7888/pypi/simple/"
default = true
```

Commit either file so team members use escrow automatically.

---

## 3. ✅ Verify it works

```bash
uv pip install --dry-run requests 2>&1 | head -5
```

Open the dashboard:

```
http://localhost:7888/dashboard
```

Packages younger than 7 days show a **Blocked** badge with an **Approve** button.

---

## 4. 🗑️ Remove escrow

**Global** (`~/.config/uv/uv.toml`): delete `index-url` and `[[index]]` entries,
or replace the URL with `https://pypi.org/simple/`.

**Per-project:** delete `uv.toml` or the relevant `[tool.uv]` block in
`pyproject.toml`.

---

## 5. 🔧 Troubleshooting

**`No solution found`** — escrow is not running. Start the proxy, then retry.

**uv ignores `uv.toml`** — uv discovers `uv.toml` by walking up from the current
directory. Make sure the file is in the project root and you are running `uv`
from within that directory tree.

**Extra index bypasses escrow** — `[[index]]` entries without `default = true` are
additive and uv may find packages there first. Remove extra index entries or set
`exclude-newer` to limit fetches.
