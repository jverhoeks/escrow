import { test, expect } from '@playwright/test';

async function login(page: import('@playwright/test').Page) {
  await page.goto('/dashboard/login');
  await page.fill('input[name="username"]', 'admin');
  await page.fill('input[name="password"]', 'admin');
  await page.click('button[type="submit"]');
  await expect(page).toHaveURL('/dashboard/');
}

test.describe('Dashboard', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('loads and shows navigation', async ({ page }) => {
    await expect(page.locator('.topbar')).toBeVisible();
    // Should have navigation tabs
    await expect(page.locator('.nav-tabs')).toBeVisible();
  });

  test('shows live feed tab', async ({ page }) => {
    await expect(page.locator('#tab-live')).toBeVisible();
  });

  test('can switch to packages view', async ({ page }) => {
    const packagesTab = page.locator('#tab-packages');
    await expect(packagesTab).toBeVisible();
    await packagesTab.click();
    await expect(page.locator('#view-packages')).toBeVisible();
  });

  test('api/me returns session info', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/me');
      return res.json();
    });
    expect(response.username).toBe('admin');
    expect(response.csrfToken).toBeTruthy();
  });

  test('api/events returns event data', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/events?n=5');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/stats returns stats data', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/stats?window=24h');
      return res.json();
    });
    expect(response).toHaveProperty('blocked');
    expect(response).toHaveProperty('warned');
    expect(response).toHaveProperty('allowed');
  });

  test('api/packages returns package list', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/packages');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/cves returns CVE list', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/cves');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/settings returns settings', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/settings');
      return res.json();
    });
    expect(response).toHaveProperty('config');
    expect(response).toHaveProperty('password_set');
    expect(response).toHaveProperty('secret_set');
  });

  test('api/accesslog returns log entries', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/accesslog?n=5');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/upstreamlog returns log entries', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/upstreamlog?n=5');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/rescan/status returns rescan status', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/rescan/status');
      return res.json();
    });
    expect(response).toHaveProperty('enabled');
    expect(response).toHaveProperty('last_run');
  });

  test('api/allowlist returns allow list', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/allowlist');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/blocklist returns block list', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/blocklist');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });
});

test.describe('Egress', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('api/egresslog returns egress data', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/egresslog?n=5');
      return res.json();
    });
    expect(Array.isArray(response)).toBe(true);
  });

  test('api/egress/stats/timeseries returns stats', async ({ page }) => {
    const response = await page.evaluate(async () => {
      const res = await fetch('/dashboard/api/egress/stats/timeseries?window=1h&bucket=5m');
      return res.json();
    });
    expect(response).toHaveProperty('total');
    expect(response).toHaveProperty('allowed');
    expect(response).toHaveProperty('blocked');
  });
});
