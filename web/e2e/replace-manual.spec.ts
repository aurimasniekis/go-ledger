import type { Page, Route } from '@playwright/test'
import { expect, test } from './fixtures'

// "This is the payment I recorded" (D45) against the real embedded UI with the API stubbed in the browser (fake data).
// A keyboard-reachable journey from the match card to the result, Undo, and the month view, at 320 CSS px.
// MATCHES_SHOTS_DIR, when set, receives screenshots for reviewers.
const now = '2026-11-21T10:00:00Z'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const NNBSP = String.fromCharCode(0x202f)
const shots = process.env.MATCHES_SHOTS_DIR

async function stub(page: Page) {
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  const calls: string[] = []
  let match: Record<string, unknown> = {
    id: 'm1', occurrence_id: 'occ-rent', transaction_id: 'tx1', amount: eur('1234.50'), origin: 'auto_rule', state: 'suggested', counts_as_evidence: false, needs_review: false,
    bank_text_redacted: false, created_by: { kind: 'system', user_id: null }, decided_by: null, decided_at: null,
    reason: 'automatic match not confirmed: description similarity 90/100 (a hint only)',
    transaction: { source: 'bank', id: 'tx1', account_id: 'acc', provider: 'fake', booking_status: 'booked', amount: eur('-1234.50'), date: '2026-11-21', booking_date: '2026-11-21', value_date: null,
      description: 'RENT NOVEMBER', counterparty: 'Landlord Ltd', no_longer_reported_at: null, superseded_by: null, countable: true },
    history: [], version: 1, created_at: now, updated_at: now,
  }
  let replaced = false
  const record = () => ({ id: 'mp1', occurrence_id: 'occ-rent', amount: eur('1234.50'), paid_on: '2026-11-20', note: null, version: 1, created_at: now, updated_at: now, replaced_by_match_id: replaced ? 'm1' : null })
  await page.route('**/api/v1/auth/session', (r) =>
    json(r, { user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } }))
  await page.route('**/api/v1/me', (r) => json(r, { kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'commitments:write', 'banking:read'] }))
  await page.route(/\/api\/v1\/occurrences\/occ-rent$/, (r) =>
    json(r, { id: 'occ-rent', commitment_id: 'c-rent', month: '2026-11', due_date: '2026-11-25', amount: eur('1234.50'), version: 1, cancelled_at: null,
      payment: { status: 'paid', bank_evidence_count: replaced ? 1 : 0, manual_evidence_count: replaced ? 0 : 1, override: null } }))
  await page.route(/\/api\/v1\/occurrences\/occ-rent\/manual-payments(\?.*)?$/, (r) => json(r, { items: [record()], next_cursor: null }))
  await page.route(/\/api\/v1\/commitments\/c-rent$/, (r) => json(r, { id: 'c-rent', name: 'Flat rent' }))
  await page.route(/\/api\/v1\/payment-matches(\?.*)?$/, (r) => {
    const state = new URL(r.request().url()).searchParams.get('state')
    return json(r, { items: match.state === state ? [match] : [], next_cursor: null })
  })
  await page.route(/\/api\/v1\/payment-matches\/m1$/, (r) => json(r, match))
  await page.route(/\/api\/v1\/payment-matches\/m1\/(confirm-replacing|unconfirm)$/, (r) => {
    const action = r.request().url().split('/').pop()!
    calls.push(action + ' ' + r.request().postData())
    if (action === 'confirm-replacing') {
      replaced = true
      match = { ...match, state: 'confirmed', version: 2, counts_as_evidence: true, decided_by: { kind: 'user', user_id: 'u1' },
        history: [{ action: 'confirmed', state: 'confirmed', amount: eur('1234.50'), by: { kind: 'user', user_id: 'u1' }, reason: null, at: now, replaced_manual_payment_ids: ['mp1'] }] }
    } else {
      replaced = false
      match = { ...match, state: 'suggested', version: 3, counts_as_evidence: false, decided_by: null }
    }
    return json(r, match)
  })
  await page.route(/\/api\/v1\/occurrences\/summary\?.*$/, (r) =>
    json(r, { month: '2026-11', as_of: '2026-11-21', freshness: { computed_at: now, occurrences_changed_at: now, bank: { available: true, note: '' } },
      totals: [{ currency: 'EUR', due: eur('1234.50'), paid: eur(replaced ? '1234.50' : '1234.50'), outstanding: eur('0.00'), overdue: eur('0.00'), overpaid: eur('0.00'), occurrence_count: 1, overdue_count: 0, unconverted_evidence_count: 0 }],
      items: [{ id: 'occ-rent', commitment_id: 'c-rent', commitment_name: 'Flat rent', month: '2026-11', due_date: '2026-11-25', amount: eur('1234.50'), version: 1,
        payment: { status: 'paid', derived_status: 'paid', timing: null, paid_amount: eur('1234.50'), outstanding_amount: eur('0.00'), overpaid_amount: eur('0.00'), shortfall_amount: eur('0.00'),
          unconverted_currencies: [], evidence_count: 1, manual_evidence_count: replaced ? 0 : 1, bank_evidence_count: replaced ? 1 : 0, grace_until: null, within_grace: false, override: null } }] }))
  return calls
}

const noOverflow = async (page: Page) => {
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
}
const shot = async (page: Page, name: string) => {
  if (shots) await page.screenshot({ path: shots + '/' + name + '.png', fullPage: true })
}

for (const size of [{ name: '320', width: 320, height: 800 }, { name: '1280', width: 1280, height: 900 }]) {
  test.describe('"This is the payment I recorded" at ' + size.name + ' px', () => {
    test.use({ viewport: { width: size.width, height: size.height } })
    test('card, form, result with provenance, Undo, and the month view show the replaced record distinctly', async ({ page }) => {
      const calls = await stub(page)
      await page.goto('/matches')
      const card = page.getByRole('article')
      await expect(card.getByTestId('already-paid')).toContainText('choose "This is the payment I recorded"')
      await noOverflow(page)
      await shot(page, 'match-card-' + size.name)

      // By keyboard: open, read what happens, submit.
      const open = card.getByRole('button', { name: 'This is the payment I recorded' })
      await open.focus()
      await page.keyboard.press('Enter')
      const form = card.getByRole('form', { name: 'This is the payment I recorded' })
      await expect(form).toContainText('kept in history and no longer counted')
      await expect(form.getByRole('checkbox')).toBeFocused()
      await expect(form).toContainText('2026-11-20')
      expect(await form.textContent()).toContain('1' + NNBSP + '234.50')
      await noOverflow(page)
      await shot(page, 'match-form-' + size.name)
      await page.keyboard.press('Escape')
      await expect(open).toBeFocused()
      expect(calls).toEqual([])
      await page.keyboard.press('Enter')
      await form.getByRole('button', { name: 'Confirm and replace my record' }).click()

      const notice = page.getByTestId('replaced-notice')
      await expect(notice).toContainText('Match confirmed for Flat rent, November 2026')
      await expect(notice).toContainText('Manual record replaced')
      await expect(notice).toContainText('kept in history and no longer counted')
      await expect(notice).toBeFocused()
      expect(calls[0]).toContain('"manual_payment_ids":["mp1"]')
      await noOverflow(page)
      await shot(page, 'match-result-' + size.name)

      // The month view shows the record apart from the bank payment.
      await page.goto('/month?month=2026-11')
      const records = page.getByTestId('replaced-records')
      await expect(records).toContainText('Manual record replaced')
      await expect(page.locator('[data-provenance="bank"]').first()).toBeVisible()
      await noOverflow(page)
      await shot(page, 'month-replaced-' + size.name)

      // Undo (unconfirm) brings the record back.
      await page.goto('/matches')
      await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
      await expect(page.getByRole('article')).toContainText('Replaced 1 manual record, kept in history.')
    })
  })
}

test('Undo right after saving uses unconfirm and says the record counts again', async ({ page }) => {
  const calls = await stub(page)
  await page.goto('/matches')
  await page.getByRole('button', { name: 'This is the payment I recorded' }).click()
  await page.getByRole('button', { name: 'Confirm and replace my record' }).click()
  await page.getByRole('button', { name: 'Undo: bring my record back' }).click()
  await expect(page.getByTestId('replaced-notice')).toContainText('counts as payment again')
  expect(calls[1]).toContain('unconfirm')
})
