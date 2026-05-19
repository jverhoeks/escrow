# 🚀 Maven → Escrow Quickstart

Routes all Maven dependency downloads through escrow (`/maven2/`), which enforces
the age gate and OSV policy server-side. Publish timestamps are looked up from the
Maven Central Search API and cached for 1 hour.

> **Age enforcement**: escrow adds server-side blocking via `maven-metadata.xml` filtering.
> Maven itself has no age gate.

---

## 1. ⚙️ Enable Maven in escrow

```toml
# escrow.toml
[ecosystems]
  maven = true
  # maven_upstream = "https://repo1.maven.org/maven2"  # optional override
```

Restart escrow. The proxy is available at `http://localhost:7888/maven2/`.

---

## 2. 🌐 Global setup (`~/.m2/settings.xml`)

```xml
<settings>
  <mirrors>
    <mirror>
      <id>escrow</id>
      <name>Escrow Proxy</name>
      <url>http://localhost:7888/maven2</url>
      <mirrorOf>central</mirrorOf>
      <checksumPolicy>fail</checksumPolicy>
    </mirror>
  </mirrors>
</settings>
```

> ⚠️ **`checksumPolicy=fail`** — default is `warn`, which lets tampered artifacts
> install silently. Always set `fail`.

---

## 3. 📁 Per-project setup

Create `settings.xml` in your project root with the same mirror block, then run:

```bash
mvn install -s settings.xml
```

Commit `settings.xml` so CI and team members use escrow automatically.

---

## 4. ✅ Verify it works

```bash
mvn dependency:resolve -s settings.xml 2>&1 | head -20
```

Open `http://localhost:7888/dashboard` — artifacts younger than 7 days show
a **Blocked** badge with an **Approve** button.

---

## 5. 🗑️ Remove escrow

**Global** (`~/.m2/settings.xml`): delete the `<mirror>` block.

**Per-project:** delete `settings.xml` or stop passing `-s settings.xml`.

---

## 6. 🔧 Troubleshooting

**`Could not transfer artifact ... Connection refused`** — escrow not running
or `maven = true` missing from `[ecosystems]`.

**`No plugin found for prefix 'X'`** — do **not** change `<mirrorOf>` to `*`.
Use `central` only. Using `*` routes Maven plugin group metadata through escrow,
which can confuse plugin prefix resolution. If you see this error, clear the
escrow disk cache (`escrow --clear-cache`) and retry.

**Snapshots not resolving** — configure `maven_snapshot_upstream` in `escrow.toml`
and set `<mirrorOf>central</mirrorOf>` (not `*`). Snapshot repos are separate
upstreams and should not share the `central` mirror.

**Maven Central rate-limits (HTTP 429)** — Maven fires many rapid HEAD probes for
POM existence checks. Escrow fetches and caches POMs on the first HEAD to prevent
this. If you still see 429s on a cold cache, wait 60 s and retry; subsequent
runs are served from cache.

**Local cache stale** — `mvn dependency:purge-local-repository` clears `~/.m2/repository`.
