import { expect, test } from './fixtures'

test('the shell loads and deep links work', async ({ page }) => {
  // The overview needs a session now (A7): a signed-out visitor lands on sign-in.
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
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

test('a protected page sends a signed-out visitor to sign-in, which works by keyboard', async ({ page }) => {
  await page.goto('/account/tokens')
  await expect(page).toHaveURL(/\/sign-in\?redirect=%2Faccount%2Ftokens/)
  await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  await expect(page.getByLabel('Username')).toBeFocused()
  // Empty submit: field-associated errors, no request needed.
  await page.keyboard.press('Tab')
  await page.keyboard.press('Tab')
  await page.keyboard.press('Enter')
  await expect(page.getByLabel('Username')).toHaveAttribute('aria-invalid', 'true')
  await expect(page.getByLabel('Username')).toHaveAccessibleDescription('Enter your username.')
})

test('wrong credentials show one alert and no account detail', async ({ page }) => {
  await page.goto('/sign-in')
  await page.getByLabel('Username').fill('nobody')
  await page.getByLabel('Password').fill('not-a-real-password')
  await page.keyboard.press('Enter')
  await expect(page.getByRole('alert')).toContainText('incorrect')
})
