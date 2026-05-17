# 🚀 Maven → Escrow Quickstart

Routes all Maven dependency downloads through escrow (`/maven2/`), which enforces
the age gate and OSV policy server-side. Publish timestamps are looked up from the
Maven Central Search API and cached for 1 hour.

> **Age enforcement**: escrow adds server-side blocking via `maven-metadata.xml` filtering.
> Maven itself has no age gate.

---

## 1. ⚙️ Enable Maven in escrow

```toml
# sentinel.toml
[ecosystems]
  maven = true
  # maven_upstream = "https://repo1.maven.org/maven2"  # optional override
```

Restart escrow. The proxy is available at `http://localhost:8888/maven2/`.

---

## 2. 🌐 Global setup (`~/.m2/settings.xml`)

```xml
<settings>
  <mirrors>
    <mirror>
      <id>escrow</id>
      <name>Escrow Proxy</name>
      <url>http://localhost:8888/maven2</url>
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

Open `http://localhost:8888/dashboard` — artifacts younger than 7 days show
a **Blocked** badge with an **Approve** button.

---

## 5. 🗑️ Remove escrow

**Global** (`~/.m2/settings.xml`): delete the `<mirror>` block.

**Per-project:** delete `settings.xml` or stop passing `-s settings.xml`.

---

## 6. 🔧 Troubleshooting

**`Could not transfer artifact ... Connection refused`** — escrow not running
or `maven = true` missing from `[ecosystems]`.

**Snapshots not resolving** — change `<mirrorOf>central</mirrorOf>` to
`<mirrorOf>*</mirrorOf>` only if you need to proxy snapshot repos too. Generally
keep it scoped to `central`.

**Local cache stale** — `mvn dependency:purge-local-repository` clears `~/.m2/repository`.
