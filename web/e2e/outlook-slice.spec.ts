import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The outlook sliced by a bucket (O3) in a real browser against the embedded UI, with the API stubbed in the browser:
// choosing a bucket asks the server for that slice, the slice is labelled as a slice and as not an allocation, and
// nothing scrolls sideways at 390 px.
const now = '2026-11-21T10:00:00Z'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const bucketId = '01JD3Y4C8V9M2Q7W5R6T0XZB00'
const bucket = { id: bucketId, kind: 'bucket', name: 'Household', parent_id: null, archived: false, archived_at: null, version: 1, created_at: now, updated_at: now }
const fresh = { state: 'fresh', stale: false, last_success_at: now, last_attempt_at: now, last_error_kind: null, last_error_code: null, notice_code: null }

const base = {
  as_of: '2026-11-21',
  notice: 'Outlook against recorded commitments: bank balances compared with the commitments Ledger has recorded as still due. It covers recorded commitments only, is not a forecast and does not tell you how much money is safe to spend.',
  slice: null as unknown,
  window: { from: '2026-10', through: '2026-11', truncated: false, not_recorded_count: 0 },
  currencies: [
    { currency: 'EUR', still_due: eur('200.00'), overdue: eur('100.00'), upcoming: eur('100.00'), overdue_count: 1, unconverted_evidence_count: 0, balance: eur('1250.40'), balance_less_still_due: eur('1050.40'),
      account_count: 1, stale_account_count: 0, oldest_balance_obtained_at: now, overdue_months: [], balance_as_of: '2026-11-20', payments_after_balance: { count: 0, amount: eur('0.00') } },
  ],
  balances: { shown: true, note: 'Balances are the latest each bank reported.', stale_after_seconds: 86400 },
  accounts: [
    { id: 'a1', connection_id: 'c1', name: 'Everyday', institution: 'Fake Bank', currency: 'EUR', counted: true, not_counted_reason: null,
      balances: [{ type: 'booked', provider_type: 'CLBD', amount: eur('1250.40'), reference_date: '2026-11-20', obtained_at: now }], freshness: fresh },
  ],
  converted: null,
  planned_outflows: { included: false, note: 'Planned outflows are not part of Ledger yet.' },
  freshness: { computed_at: now, occurrences_changed_at: now },
}
const slice = {
  bucket: { id: bucketId, name: 'Household', archived: false },
  commitment_count: 2,
  account_count: 1,
  note: 'Slice of the outlook: only the commitments and the accounts in this bucket are counted. A bucket allocates no money: each balance is the whole balance of an account in the bucket, not a share of it.',
}

async function stub(page: Page, asked: string[]) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'connections:read', 'organisation:read'] })))
  await page.route('**/api/v1/buckets*', (r) => r.fulfill(body({ items: [bucket], next_cursor: null })))
  await page.route('**/api/v1/outlook*', (r) => {
    const b = new URL(r.request().url()).searchParams.get('bucket')
    asked.push(b ?? '')
    return r.fulfill(body(b ? { ...base, slice } : base))
  })
}

test('choosing a bucket slices the outlook and labels it as a slice', async ({ page }) => {
  const asked: string[] = []
  await stub(page, asked)
  await page.goto('/outlook')
  await expect(page.getByTestId('outlook-notice')).toBeVisible()
  await expect(page.getByTestId('outlook-slice')).toHaveCount(0)
  await page.getByLabel('Show only one bucket (a slice of the outlook)').selectOption({ label: 'Household' })
  const note = page.getByTestId('outlook-slice')
  await expect(note).toContainText('Only the bucket Household')
  await expect(note).toContainText('Not the whole outlook')
  await expect(note).toContainText('A bucket allocates no money')
  await expect(page).toHaveURL(new RegExp('bucket=' + bucketId))
  expect(asked.at(-1)).toBe(bucketId)
  await note.getByRole('link', { name: 'Show the whole outlook' }).click()
  await expect(page.getByTestId('outlook-slice')).toHaveCount(0)
  expect(asked.at(-1)).toBe('')
})

test('at 390 px the slice label does not scroll sideways', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stub(page, [])
  await page.goto('/outlook?bucket=' + bucketId)
  await expect(page.getByTestId('outlook-slice')).toBeVisible()
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
})
