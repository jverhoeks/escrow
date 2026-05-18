# 🚀 NuGet (.NET) → Escrow Quickstart

Routes all NuGet package restores through escrow, which enforces the age gate and
OSV vulnerability policy server-side.

> **Age enforcement**: escrow adds server-side blocking. NuGet itself has no age gate.
> **XML comments**: NuGet config files use `<!-- -->` comments; do not use `--` inside
> a comment (XML parser rejects it).

---

## 1. ⚙️ Enable NuGet in escrow

```toml
# escrow.toml
[ecosystems]
  nuget = true
  # nuget_upstream = "https://api.nuget.org/v3"  # optional override
```

Restart escrow. The proxy is now available at `http://localhost:7888/nuget/index.json`.

---

## 2. 🌐 Global setup (all projects on this machine)

```bash
dotnet nuget add source http://localhost:7888/nuget/index.json --name escrow
dotnet nuget disable source nuget.org
```

Verify:
```bash
dotnet nuget list source
# escrow [Enabled]
#   http://localhost:7888/nuget/index.json
```

---

## 3. 📁 Per-project setup

Create `nuget.config` in your project root:

```xml
<?xml version="1.0" encoding="utf-8"?>
<configuration>
  <packageSources>
    <clear />
    <add key="escrow" value="http://localhost:7888/nuget/index.json" />
  </packageSources>
</configuration>
```

`<clear />` removes all inherited sources so only escrow is used.
Commit `nuget.config` so the whole team uses escrow automatically.

---

## 4. ✅ Verify it works

```bash
dotnet restore
```

Open `http://localhost:7888/dashboard` — packages younger than 7 days show
a red **Blocked** badge with an **Approve** button.

---

## 5. 🗑️ Remove escrow

**Global:**
```bash
dotnet nuget remove source escrow
dotnet nuget enable source nuget.org
```

**Per-project:** delete `nuget.config` or restore the original content.

---

## 6. 🔧 Troubleshooting

**`Unable to load the service index`** — escrow is not running or NuGet is not
enabled (`nuget = true` in `[ecosystems]`).

**Package not found** — the age gate may be blocking it. Check the dashboard
and click **Approve** to allow it through.

**`nuget.org` still resolves packages** — verify `<clear />` is present.
Without it, NuGet merges sources from parent config files.
