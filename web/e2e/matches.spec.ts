import type { Page, Route } from '@playwright/test'
import { expect, test } from './fixtures'

// The /matches screen against the real embedded UI with the API stubbed in the browser (fake data, no database, no bank).
// One keyboard journey, then reflow at 320 CSS px, with 200% text, and at 200% zoom (WCAG 1.4.10, 1.4.4).
const now = '2026-11-21T10:00:00Z'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const system = { kind: 'system', user_id: null }
const ambiguous =
  'automatic match not confirmed: several_transactions (2 candidate transactions for this occurrence); date_outside_window (9 days from the due date; at most 3); description similarity 80/100 (a hint only)'

type Fake = Record<string, unknown> & { id: string; state: string; version: number }
function bankMatch(id: string, over: Record<string, unknown> = {}): Fake {
  return {
    id, occurrence_id: 'occ-' + id, transaction_id: 'tx-' + id, amount: eur('20.00'), origin: 'auto_rule', state: 'suggested',
    counts_as_evidence: false, needs_review: false, bank_text_redacted: false, created_by: system, decided_by: null, decided_at: null, reason: ambiguous,
    transaction: {
      source: 'bank', id: 'tx-' + id, account_id: 'acc', provider: 'fake', booking_status: 'booked', amount: eur('-20.00'), date: '2026-11-24',
      booking_date: '2026-11-24', value_date: null, description: 'SERVER HOSTING INVOICE 2026-11 WITH A LONG REFERENCE NUMBER 0000111122223333', counterparty: 'Hosting Provider Ltd',
      no_longer_reported_at: null, superseded_by: null, countable: true,
    },
    history: [], version: 1, created_at: now, updated_at: now,
    ...over,
  }
}

async function stub(page: Page) {
  const json = (route: Route, body: unknown, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) })
  let matches: Fake[] = [
    bankMatch('a', { occurrence_id: 'occ-host' }),
    bankMatch('b', { occurrence_id: 'occ-host' }),
    bankMatch('c', { state: 'confirmed', version: 2, counts_as_evidence: true, decided_by: system, reason: null, amount: eur('800.00'),
      history: [{ action: 'confirmed', state: 'confirmed', amount: eur('800.00'), by: system, reason: null, at: '2026-11-21T09:30:00Z' }] }),
  ]
  const posts: string[] = []
  await page.route('**/api/v1/auth/session', (r) =>
    json(r, { user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } }))
  await page.route('**/api/v1/me', (r) => json(r, { kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'commitments:write', 'banking:read'] }))
  await page.route(/\/api\/v1\/occurrences\/occ-[a-z]+$/, (r) => {
    const id = r.request().url().split('/').pop()!
    return json(r, { id, commitment_id: 'c-' + id, month: '2026-11', due_date: '2026-11-25', amount: eur('20.00'), version: 1, cancelled_at: null })
  })
  await page.route(/\/api\/v1\/commitments\/c-occ-[a-z]+$/, (r) => json(r, { id: 'c', name: r.request().url().endsWith('host') ? 'Server hosting with a rather long commitment name' : 'Flat rent' }))
  await page.route(/\/api\/v1\/payment-matches(\?.*)?$/, (r) => {
    const state = new URL(r.request().url()).searchParams.get('state')
    return json(r, { items: matches.filter((m) => m.state === state), next_cursor: null })
  })
  await page.route(/\/api\/v1\/payment-matches\/[a-z]+\/(confirm|reject|unmatch|correct|reverse)$/, (r) => {
    const [, id, action] = /payment-matches\/([a-z]+)\/([a-z]+)$/.exec(r.request().url())!
    posts.push(action + ':' + id)
    const next = { confirm: 'confirmed', reject: 'rejected', unmatch: 'rejected', reverse: 'reversed', correct: 'confirmed' }[action!]!
    const m = matches.find((x) => x.id === id)!
    const saved = { ...m, state: next, version: m.version + 1, counts_as_evidence: next === 'confirmed', decided_by: { kind: 'user', user_id: 'u1' } }
    matches = matches.map((x) => (x.id === id ? saved : x))
    return json(r, saved)
  })
  return posts
}

const noOverflow = async (page: Page) => {
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
}

test('review queue by keyboard: ambiguity is explained, confirm names what changed, the engine and a person are told apart', async ({ page }) => {
  const posts = await stub(page)
  await page.goto('/matches')
  await expect(page.getByRole('heading', { level: 1, name: 'Payment matches' })).toBeVisible()
  const cards = page.getByRole('article')
  await expect(cards).toHaveCount(2)
  await expect(cards.first().getByTestId('competing')).toContainText('2 suggestions compete for this occurrence')
  await expect(cards.first()).toContainText('It is 9 days from the due date; automatic confirmation allows at most 3.')
  await expect(cards.first()).not.toContainText('several_transactions')

  // Reject opens a form by keyboard, Escape closes it, and nothing was sent.
  const reject = cards.first().getByRole('button', { name: 'Reject suggestion' })
  await reject.focus()
  await page.keyboard.press('Enter')
  await expect(cards.first().getByLabel('Reason (optional)')).toBeFocused()
  await page.keyboard.type('not this one')
  await page.keyboard.press('Escape')
  await expect(reject).toBeFocused()
  expect(posts).toEqual([])

  // Confirm by keyboard: the typed reason did not leak, and the notice takes focus and names the commitment and month.
  const confirm = cards.first().getByRole('button', { name: 'Confirm match' })
  await confirm.focus()
  await page.keyboard.press('Enter')
  const notice = page.getByRole('status').filter({ hasText: 'Match confirmed' })
  await expect(notice).toContainText('Match confirmed for')
  await expect(notice).toContainText('November 2026')
  await expect(notice).toContainText('Unmatching below is final for now')
  await expect(notice).toBeFocused()
  expect(posts).toEqual(['confirm:a'])

  // The Confirmed list tells the engine's confirmation from the person's.
  await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
  const engine = page.locator('article[data-match-id="c"]')
  await expect(engine.getByTestId('decision')).toHaveText('Confirmed automatically by the matching engine, after its safeguards passed.')
  await expect(engine).toContainText('Decided by the engine')
  const mine = page.locator('article[data-match-id="a"]')
  await expect(mine.getByTestId('decision')).toHaveText('Confirmed by you.')
  await expect(mine).not.toContainText('Decided by the engine')
})

const sizes = [
  { name: '320 CSS px wide', viewport: { width: 320, height: 640 }, textScale: false },
  { name: '200% page zoom (1280 px at 200% is 640 CSS px)', viewport: { width: 640, height: 360 }, textScale: false },
  { name: '320 CSS px wide with 200% text size', viewport: { width: 320, height: 640 }, textScale: true },
]
for (const s of sizes) {
  test.describe('/matches at ' + s.name, () => {
    test.use({ viewport: s.viewport })
    test('both lists and an open form reflow: nothing scrolls sideways or leaves the viewport', async ({ page }) => {
      await stub(page)
      await page.goto('/matches')
      if (s.textScale) await page.addStyleTag({ content: 'html { font-size: 200% }' })
      await expect(page.getByRole('article')).toHaveCount(2)
      await noOverflow(page)
      await page.getByRole('button', { name: 'Reject suggestion' }).first().click()
      await expect(page.getByLabel('Reason (optional)')).toBeVisible()
      await noOverflow(page)
      await page.getByRole('button', { name: 'Cancel' }).click()
      await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
      await expect(page.locator('article[data-match-id="c"]')).toBeVisible()
      await page.getByRole('button', { name: 'Correct amount' }).click()
      await expect(page.getByLabel(/Allocated amount/)).toBeVisible()
      await noOverflow(page)
      for (const b of await page.getByRole('button').all()) {
        const box = await b.boundingBox()
        if (!box) continue
        expect(box.x).toBeGreaterThanOrEqual(0)
        expect(box.x + box.width).toBeLessThanOrEqual(s.viewport.width)
      }
    })
  })
}
