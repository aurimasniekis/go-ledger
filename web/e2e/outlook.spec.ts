import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The outlook (FX3, FX3c) in a real browser against the embedded UI, with the API stubbed in the browser: the balance
// date and the caveat about payments recorded after it, the overdue drill-down into the monthly view, the converted
// section that shows only its explanation when nothing converts, and the month navigation on one row at 390 px.
// OUTLOOK_SHOTS_DIR, when set, receives screenshots for reviewers.
const now = '2026-11-21T10:00:00Z'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const gbp = (amount: string) => ({ amount, currency: 'GBP' })
const fresh = { state: 'fresh', stale: false, last_success_at: '2026-11-21T08:00:00Z', last_attempt_at: '2026-11-21T08:00:00Z', last_error_kind: null, last_error_code: null, notice_code: null }

const outlook = {
  as_of: '2026-11-21',
  notice: 'Outlook against recorded commitments: bank balances compared with the commitments Ledger has recorded as still due. It covers recorded commitments only, is not a forecast and does not tell you how much money is safe to spend.',
  window: { from: '2025-12', through: '2026-11', truncated: false, not_recorded_count: 2 },
  currencies: [
    { currency: 'EUR', still_due: eur('1335.00'), overdue: eur('335.00'), upcoming: eur('1000.00'), overdue_count: 3, unconverted_evidence_count: 0, balance: eur('1250.40'), balance_less_still_due: eur('-84.60'),
      account_count: 1, stale_account_count: 1, oldest_balance_obtained_at: '2026-11-19T08:00:00Z',
      overdue_months: [{ month: '2026-09', count: 1, amount: eur('100.00') }, { month: '2026-10', count: 2, amount: eur('235.00') }],
      balance_as_of: '2026-10-31', payments_after_balance: { count: 1, amount: eur('850.00') } },
    { currency: 'GBP', still_due: gbp('25.00'), overdue: gbp('0.00'), upcoming: gbp('25.00'), overdue_count: 0, unconverted_evidence_count: 0, balance: gbp('300.00'), balance_less_still_due: gbp('275.00'),
      account_count: 1, stale_account_count: 0, oldest_balance_obtained_at: '2026-11-21T08:00:00Z', overdue_months: [], balance_as_of: '2026-11-21', payments_after_balance: { count: 0, amount: gbp('0.00') } },
  ],
  balances: { shown: true, note: "Balances are the latest each bank reported; check each account's freshness before relying on them.", stale_after_seconds: 86400 },
  accounts: [
    { id: 'a1', connection_id: 'c1', name: 'Everyday', institution: 'Fake Bank', currency: 'EUR', counted: true, not_counted_reason: null,
      balances: [{ type: 'booked', provider_type: 'CLBD', amount: eur('1250.40'), reference_date: '2026-10-31', obtained_at: '2026-11-19T08:00:00Z' }], freshness: { ...fresh, state: 'stale', stale: true } },
    { id: 'a2', connection_id: 'c1', name: 'Pounds', institution: 'Fake Bank', currency: 'GBP', counted: true, not_counted_reason: null,
      balances: [{ type: 'booked', provider_type: 'CLBD', amount: gbp('300.00'), reference_date: null, obtained_at: '2026-11-21T08:00:00Z' }], freshness: fresh },
  ],
  converted: {
    currency: 'EUR', complete: false, still_due: eur('1335.00'), overdue: eur('335.00'), balance: eur('1250.40'), balance_less_still_due: eur('-84.60'), balance_account_count: 1, stale_balance_account_count: 1,
    included: [{ currency: 'EUR', rate: null, age_seconds: null, from_cache: false, refresh_failed: false }],
    excluded: [{ currency: 'GBP', status: 'missing', reason: 'no_provider', rate: null }],
  },
  planned_outflows: { included: false, note: 'Planned outflows (deferral plans) are not part of Ledger yet; only recorded commitments are compared.' },
  freshness: { computed_at: now, occurrences_changed_at: now },
}

async function stub(page: Page) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'connections:read'] })))
  await page.route('**/api/v1/outlook*', (r) => r.fulfill(body(outlook)))
  await page.route('**/api/v1/occurrences/summary*', (r) =>
    r.fulfill(body({ month: '2026-10', as_of: '2026-11-21', items: [], totals: [], freshness: { computed_at: now, occurrences_changed_at: null, bank: { available: false, note: '' } } })))
}

const shot = async (page: Page, name: string) => {
  const dir = process.env.OUTLOOK_SHOTS_DIR
  if (dir) await page.screenshot({ path: dir + '/' + name + '.png', fullPage: true })
}

test('the outlook shows each balance with its date, the payments after it and the overdue months', async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 })
  await stub(page)
  await page.goto('/outlook')
  const card = page.getByTestId('outlook-EUR')
  await expect(card.getByText('Bank balance, as of 2026-10-31')).toBeVisible()
  await expect(page.getByTestId('outlook-after-EUR')).toContainText('1 payment')
  await expect(page.getByTestId('outlook-after-EUR')).toContainText('already off "still due" but may not be in the bank balance yet')
  await expect(card.getByText('1 of 1 balance not refreshed in 24 h')).toBeVisible()
  await expect(page.getByTestId('outlook-not-recorded')).toContainText('2 occurrences due before tracking started are not counted')
  // Nothing besides EUR converts: only the explanation, no repeated figures.
  const converted = page.getByRole('region', { name: 'Converted to EUR (secondary)' })
  await expect(converted).toContainText('no other currency could be converted')
  await expect(converted.locator('dl')).toHaveCount(0)
  await shot(page, 'outlook-desktop')
  await card.getByRole('link', { name: 'October 2026' }).click()
  await expect(page).toHaveURL(/\/month\?month=2026-10$/)
})

test('at 390 px the month navigation stays on one row and nothing scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stub(page)
  await page.goto('/outlook')
  const group = page.getByRole('group', { name: 'Commitments counted through' })
  await expect(group).toContainText('Everything still owed since your first commitment started (December 2025), through November 2026')
  const tops = await group.locator('button, p').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top + e.getBoundingClientRect().height / 2)))
  expect(tops.length).toBe(3)
  // The two buttons and the label are centred on one row: the label is taller, but the centres are close.
  expect(Math.max(...tops) - Math.min(...tops), 'buttons and label share a row').toBeLessThanOrEqual(30)
  const buttons = await group.getByRole('button').evaluateAll((els) => els.map((e) => Math.round(e.getBoundingClientRect().top)))
  expect(buttons[0]).toBe(buttons[1])
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
  await shot(page, 'outlook-mobile-390')
})
