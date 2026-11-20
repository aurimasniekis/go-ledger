import { expect, test } from '@playwright/test'

test('the shell loads and deep links work', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Ledger' })).toBeVisible()
  await page.goto('/no/such/page')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
})

test('unknown API paths return the JSON error envelope', async ({ request }) => {
  const res = await request.get('/api/nope')
  expect(res.status()).toBe(404)
  expect((await res.json()).code).toBe('not_found')
})
