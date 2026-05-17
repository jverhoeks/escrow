# 🚀 Composer → Escrow Quickstart

Routes all Composer package installs through the escrow proxy, which enforces a
7-day age gate server-side on every Packagist package fetch.

> **Age enforcement**: escrow adds server-side blocking. Composer itself has no
> built-in age gate.

---

## 1. 🌐 Global setup

```bash
composer config --global repositories.escrow \
  '{"type":"composer","url":"http://localhost:7888/composer"}'
composer config --global repo.packagist false
```

The second line disables direct access to packagist.org so all traffic flows
through escrow.

Verify:
```bash
composer config --global --list | grep repositories
```

---

## 2. 📁 Per-project setup

Add the repository to your project's `composer.json`:

```json
{
    "repositories": [
        {
            "type": "composer",
            "url": "http://localhost:7888/composer"
        },
        {
            "packagist.org": false
        }
    ]
}
```

Commit `composer.json` so team members use escrow automatically.

---

## 3. ✅ Verify it works

```bash
composer require --dry-run monolog/monolog 2>&1 | head -10
```

Open the dashboard:

```
http://localhost:7888/dashboard
```

Packages younger than 7 days show a **Blocked** badge with an **Approve** button.

---

## 4. 🗑️ Remove escrow

**Global:**
```bash
composer config --global --unset repositories.escrow
composer config --global repo.packagist true
```

**Per-project:** remove the escrow entry from the `repositories` array in
`composer.json` and delete the `"packagist.org": false` line.

---

## 5. 🔧 Troubleshooting

**`[Composer\Downloader\TransportException] ... Connection refused`** — escrow is
not running. Start the proxy before running `composer install`.

**Packagist still reachable despite `"packagist.org": false`** — the global config
may still have packagist enabled. Run `composer config --global repo.packagist false`
to disable it globally.

**`Your requirements could not be resolved`** — the package version is blocked by
the age gate. Check the dashboard, approve it, and retry.
