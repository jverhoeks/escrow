# 🚀 Gradle → Escrow Quickstart

Routes all Gradle dependency downloads through escrow (`/maven2/`). Escrow uses the
same Maven 2 layout as Maven Central, so Gradle and Maven share the same proxy endpoint.

> **Age enforcement**: escrow adds server-side blocking via `maven-metadata.xml` filtering.
> Gradle itself has no age gate.

---

## 1. ⚙️ Enable Maven in escrow (shared with Gradle)

```toml
# sentinel.toml
[ecosystems]
  maven = true
  # maven_upstream = "https://repo1.maven.org/maven2"  # optional override
```

Restart escrow. The proxy is available at `http://localhost:7888/maven2/`.

---

## 2. 📁 Per-project setup (Kotlin DSL)

```kotlin
// settings.gradle.kts
dependencyResolutionManagement {
    repositories {
        maven(url = "http://localhost:7888/maven2")
        // Do NOT add mavenCentral() — that bypasses the proxy
    }
}
```

```kotlin
// build.gradle.kts
repositories {
    maven(url = "http://localhost:7888/maven2")
}
```

---

## 3. 🌐 Global setup (init script — affects all Gradle projects)

Create `~/.gradle/init.d/escrow.gradle`:

```groovy
allprojects {
    buildscript {
        repositories {
            maven { url 'http://localhost:7888/maven2' }
        }
    }
    repositories {
        maven { url 'http://localhost:7888/maven2' }
    }
}
```

---

## 4. 🔐 Add dependency verification (recommended)

```kotlin
// build.gradle.kts
dependencyLocking {
    lockAllConfigurations()
    lockMode.set(LockMode.STRICT)
}
```

```properties
# gradle.properties
org.gradle.dependency.verification=strict
```

Generate metadata once and commit it:
```bash
./gradlew --write-verification-metadata sha256 dependencies
git add gradle/verification-metadata.xml
```

---

## 5. ✅ Verify it works

```bash
./gradlew dependencies 2>&1 | head -20
```

Open `http://localhost:7888/dashboard` — artifacts younger than 7 days show
a **Blocked** badge with an **Approve** button.

---

## 6. 🗑️ Remove escrow

**Per-project:** add `mavenCentral()` back to the repositories block and remove
the escrow maven URL.

**Global:** delete `~/.gradle/init.d/escrow.gradle`.

---

## 7. 🔧 Troubleshooting

**`Could not resolve ...`** — escrow not running or `maven = true` missing in config.

**`Dependency verification failed`** — regenerate `verification-metadata.xml` if
dependencies changed: `./gradlew --write-verification-metadata sha256 dependencies`.

**Locking vs verification** — locking pins which version resolves; verification
checks that the downloaded JAR matches a known SHA256. Both are needed.
