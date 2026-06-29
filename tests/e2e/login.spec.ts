import { test, expect } from '@playwright/test';

test.describe('Login', () => {
  test('shows login page', async ({ page }) => {
    await page.goto('/dashboard/login');
    await expect(page.locator('form')).toBeVisible();
    await expect(page.locator('input[name="username"]')).toBeVisible();
    await expect(page.locator('input[name="password"]')).toBeVisible();
    await expect(page.locator('button[type="submit"]')).toBeVisible();
  });

  test('rejects invalid credentials', async ({ page }) => {
    await page.goto('/dashboard/login');
    await page.fill('input[name="username"]', 'admin');
    await page.fill('input[name="password"]', 'wrong-password');
    await page.click('button[type="submit"]');
    await page.waitForURL(/error=/, { timeout: 10000 });
  });

  test('logs in via API and accesses dashboard', async ({ page }) => {
    // Use the API directly to login
    const response = await page.request.post('/dashboard/login', {
      form: { username: 'admin', password: 'admin' },
      maxRedirects: 0,
    });
    expect(response.status()).toBe(302);
    const location = response.headers()['location'];
    expect(location).toBe('/dashboard/');

    // Now navigate to the dashboard with the session cookie set
    await page.goto('/dashboard/');
    await expect(page.locator('.topbar')).toBeVisible();
  });

  test('logs out', async ({ page }) => {
    // Login via API
    await page.request.post('/dashboard/login', {
      form: { username: 'admin', password: 'admin' },
    });
    await page.goto('/dashboard/logout');
    await page.waitForURL('/dashboard/login', { timeout: 10000 });
  });
});
