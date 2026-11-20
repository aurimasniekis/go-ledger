import { expect, test } from '@playwright/test'

test('the shell loads and deep links work', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { level: 1, name: 'Ledger' })).toBeVisible()
  await page.goto('/no/such/page')
  await expect(page.getByRole('heading', { name: 'Page not found' })).toBeVisible()
})

test('the theme switch works by keyboard, persists, and the design page follows it', async ({ page }) => {
  await page.goto('/design')
  const select = page.getByLabel('Theme')
  await select.focus()
  await page.keyboard.press('ArrowDown') // system -> light
  await page.keyboard.press('ArrowDown') // light -> dark
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await page.reload()
  await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark')
  await expect(page.getByLabel('Theme')).toHaveValue('dark')
  const bg = await page.evaluate(() => getComputedStyle(document.body).backgroundColor)
  expect(bg).toBe('rgb(1, 22, 39)')
})

test('reduced motion is honoured', async ({ page }) => {
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await page.goto('/design')
  const d = await page.getByRole('button', { name: 'Primary' }).evaluate((e) => getComputedStyle(e).transitionDuration)
  expect(parseFloat(d)).toBeLessThan(0.001)
})

test('unknown API paths return the JSON error envelope', async ({ request }) => {
  const res = await request.get('/api/nope')
  expect(res.status()).toBe(404)
  expect((await res.json()).code).toBe('not_found')
})
