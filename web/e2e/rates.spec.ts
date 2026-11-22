import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The exchange-rates screen (FX2c) in a real browser against the embedded UI, with the API stubbed in the browser:
// exact rate values, ISO rate dates, retrieval time, the stale mark, the provider status and a failed last refresh,
// and no sideways scrolling at 390 px. RATES_SHOTS_DIR, when set, receives screenshots for reviewers.
const now = '2026-11-21T10:00:00Z'

const rate = (base: string, value: string, date: string, retrieved: string, stale: string | null) => ({
  provider: 'frankfurter', source: 'ECB', base, quote: 'EUR', value, date, retrieved_at: retrieved, first_retrieved_at: retrieved,
  confirmed_through: date, stale: stale !== null, stale_reason: stale, previous: null,
})

const status = {
  enabled: true, provider: 'frankfurter', source: 'ECB', reporting_currency: 'EUR', refresh_available: true, refresh_interval_seconds: 86400,
  refresh_after_seconds: 86400, stale_after_days: 5, as_of: now, last_success_at: '2026-11-20T09:00:00Z',
  last_run: { trigger: 'scheduled', started_at: '2026-11-21T09:00:00Z', finished_at: '2026-11-21T09:00:15Z', outcome: 'failed', currencies_refreshed: 0, error: { kind: 'unavailable', code: 'timeout' } },
}

async function stub(page: Page) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'rates:read', 'rates:write'] })))
  await page.route('**/api/v1/rates/status', (r) => r.fulfill(body(status)))
  await page.route('**/api/v1/rates', (r) => r.fulfill(body({
    items: [rate('GBP', '1.16210', '2026-11-13', '2026-11-14T09:00:00Z', 'not_retrieved_recently'), rate('USD', '0.90992', '2026-11-20', '2026-11-21T09:00:00Z', null)],
    next_cursor: null,
  })))
  await page.route('**/api/v1/occurrences/summary*', (r) =>
    r.fulfill(body({ month: '2026-11', as_of: '2026-11-21', items: [], totals: [], freshness: { computed_at: now, occurrences_changed_at: null, bank: { available: false, note: '' } } })))
}

const shot = async (page: Page, name: string) => {
  const dir = process.env.RATES_SHOTS_DIR
  if (dir) await page.screenshot({ path: dir + '/' + name + '.png', fullPage: true })
}

test('the rates screen shows exact rates, ISO dates, retrieval time, staleness and a failed refresh', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await stub(page)
  await page.goto('/month')
  await page.getByRole('button', { name: 'Account' }).click()
  await page.getByRole('link', { name: 'Exchange rates' }).click()
  await expect(page).toHaveURL(/\/rates$/)
  const table = page.getByRole('table', { name: /Cached exchange rates/ })
  const gbp = table.getByRole('row', { name: /GBP\/EUR/ })
  await expect(gbp).toContainText('1.16210')
  await expect(gbp).toContainText('2026-11-13')
  await expect(gbp).toContainText('2026-11-14 09:00 UTC')
  await expect(gbp).toContainText('Stale: not retrieved recently')
  const usd = table.getByRole('row', { name: /USD\/EUR/ })
  await expect(usd).toContainText('0.90992')
  await expect(usd).toContainText('Current')
  await expect(page.getByTestId('rates-last-run')).toContainText('scheduled refresh failed: the rate provider did not answer (timeout)')
  await expect(page.getByTestId('rates-last-run')).toContainText('Last fully successful refresh: 2026-11-20 09:00 UTC')
  await expect(page.getByRole('button', { name: 'Refresh now' })).toBeEnabled()
  await shot(page, 'rates-desktop')
})

test('Refresh now shows success, then the retry time when rate limited', async ({ page }) => {
  await stub(page)
  let posts = 0
  await page.route('**/api/v1/rates/refreshes', (r) => {
    posts++
    if (posts === 1) {
      return r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ trigger: 'manual', started_at: now, finished_at: now, outcome: 'succeeded', currencies_refreshed: 2, error: null }) })
    }
    return r.fulfill({ status: 429, contentType: 'application/json', headers: { 'Retry-After': '60' }, body: JSON.stringify({ code: 'rate_limited', message: 'slow down', request_id: 'r' }) })
  })
  await page.goto('/rates')
  const button = page.getByRole('button', { name: 'Refresh now' })
  await button.click()
  await expect(page.getByTestId('rates-refresh-result')).toContainText('Refreshed 2 currencies at')
  await button.click()
  await expect(page.getByRole('alert')).toContainText('You can try again at')
  await expect(button).toHaveAttribute('aria-disabled', 'true')
})

test('at 390 px nothing scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stub(page)
  await page.goto('/rates')
  await expect(page.getByRole('table', { name: /Cached exchange rates/ })).toBeVisible()
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
  await shot(page, 'rates-mobile')
})
