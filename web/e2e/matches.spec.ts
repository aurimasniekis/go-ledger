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
    bankMatch('r', { occurrence_id: 'occ-rent', state: 'rejected', version: 2, reason: 'wrong month',
      decided_by: { kind: 'token', user_id: 'u1', via: 'mcp' },
      history: [{ action: 'rejected', state: 'rejected', amount: eur('20.00'), by: { kind: 'token', user_id: 'u1', via: 'mcp' }, reason: 'wrong month', at: '2026-11-21T09:40:00Z' }] }),
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
    return json(r, { id, commitment_id: 'c-' + id, month: '2026-11', due_date: '2026-11-25', amount: eur('20.00'), version: 1, cancelled_at: null,
      payment: (() => {
        const bank = matches.filter((m) => m.occurrence_id === id && m.state === 'confirmed' && m.counts_as_evidence).length
        return { status: bank > 0 ? 'paid' : 'unpaid', bank_evidence_count: bank, manual_evidence_count: 0, override: null }
      })() })
  })
  await page.route(/\/api\/v1\/commitments\/c-occ-[a-z]+$/, (r) => json(r, { id: 'c', name: r.request().url().endsWith('host') ? 'Server hosting with a rather long commitment name' : 'Flat rent' }))
  await page.route(/\/api\/v1\/payment-matches(\?.*)?$/, (r) => {
    const states = new URL(r.request().url()).searchParams.getAll('state')
    return json(r, { items: matches.filter((m) => states.includes(m.state)), next_cursor: null })
  })
  await page.route(/\/api\/v1\/payment-matches\/[a-z]+\/(confirm|reject|unmatch|correct|reverse|unconfirm)$/, (r) => {
    const [, id, action] = /payment-matches\/([a-z]+)\/([a-z]+)$/.exec(r.request().url())!
    posts.push(action + ':' + id)
    const next = { confirm: 'confirmed', reject: 'rejected', unmatch: 'rejected', reverse: 'reversed', correct: 'confirmed', unconfirm: 'suggested' }[action!]!
    const m = matches.find((x) => x.id === id)!
    const saved = { ...m, state: next, version: m.version + 1, counts_as_evidence: next === 'confirmed', decided_by: next === 'suggested' ? null : { kind: 'user', user_id: 'u1', via: 'session' } }
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
  // The two competing suggestions are two bank transactions: their short refs must differ and each carries the full id.
  const refA = page.locator('article[data-match-id="a"]').getByTestId('bank-ref')
  const refB = page.locator('article[data-match-id="b"]').getByTestId('bank-ref')
  await expect(refA).toHaveAttribute('title', 'tx-a')
  await expect(refB).toHaveAttribute('title', 'tx-b')
  const [shortA, shortB] = [await refA.textContent(), await refB.textContent()]
  expect(shortA).toBeTruthy()
  expect(shortB).toBeTruthy()
  expect(shortA).not.toBe(shortB)
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
  await expect(notice).toContainText('Unmatch in the Confirmed list is final')
  await expect(notice).toBeFocused()
  expect(posts).toEqual(['confirm:a'])

  // R2: the competitor is told the occurrence is already paid, instead of keeping a stale "choose" reason and a primary Confirm.
  const competitor = page.locator('article[data-match-id="b"]')
  await expect(competitor.getByTestId('already-paid')).toContainText('Another bank transaction already pays this occurrence')
  await expect(competitor.getByTestId('competing')).toHaveCount(0)
  await expect(competitor.getByRole('button', { name: 'Reject suggestion' })).toHaveClass(/bg-primary/)
  await expect(competitor.getByRole('button', { name: 'Confirm match' })).not.toHaveClass(/bg-primary/)

  // The Confirmed list tells the engine's confirmation from the person's.
  await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
  const engine = page.locator('article[data-match-id="c"]')
  await expect(engine.getByTestId('decision')).toHaveText('Confirmed automatically by the matching engine, after its safeguards passed.')
  await expect(engine).toContainText('Decided by the engine')
  const mine = page.locator('article[data-match-id="a"]')
  await expect(mine.getByTestId('decision')).toHaveText('Confirmed by you.')
  await expect(mine).not.toContainText('Decided by the engine')
})

test('a refused undo that takes its Undo button away moves focus to the notice, not to the page (focus)', async ({ page }) => {
  await stub(page)
  await page.route(/\/api\/v1\/payment-matches\/a\/unconfirm$/, (r) =>
    r.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ code: 'match_undo_window_closed', message: 'closed', request_id: 'r1' }) }))
  await page.goto('/matches')
  await page.getByRole('article').first().getByRole('button', { name: 'Confirm match' }).click()
  const notice = page.getByRole('status').filter({ hasText: 'Match confirmed' })
  await expect(notice).toBeFocused()
  await notice.getByRole('button', { name: 'Undo confirmation' }).click()
  await expect(notice.getByRole('alert')).toContainText('Undo is no longer possible')
  await expect(notice.getByRole('button', { name: 'Undo confirmation' })).toHaveCount(0)
  await expect(notice).toBeFocused()
})

test('a conflict while the Reject form is open leaves no enabled submit and puts focus on the explanation (R1)', async ({ page }) => {
  const posts = await stub(page)
  await page.route(/\/api\/v1\/payment-matches\/a\/reject$/, (r) => {
    const current = { ...bankMatch('a', { occurrence_id: 'occ-host' }), state: 'rejected', version: 2, decided_by: { kind: 'user', user_id: 'u2' } }
    return r.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ code: 'version_conflict', message: 'The match changed since you read it.', request_id: 'r1', reconciliation: { current_version: 2, current } }) })
  })
  await page.goto('/matches')
  const card = page.locator('article[data-match-id="a"]')
  await card.getByRole('button', { name: 'Reject suggestion' }).click()
  await card.getByLabel('Reason (optional)').fill('not this one')
  await card.getByRole('button', { name: 'Reject now' }).click()
  const alert = card.getByRole('alert')
  await expect(alert).toContainText('Not saved: changed elsewhere')
  await expect(alert).toBeFocused()
  await expect(card.getByRole('button', { name: 'Reject now' })).toHaveCount(0)
  await expect(card.getByRole('form')).toHaveCount(0)
  expect(posts).toEqual([])
})

test('confirm, undo and the suggestion is back; the Closed list shows a rejected match, read only, with how it came', async ({ page }, info) => {
  const posts = await stub(page)
  await page.goto('/matches')
  const cards = page.getByRole('article')
  await expect(cards).toHaveCount(2)
  await cards.first().getByRole('button', { name: 'Confirm match' }).click()
  const notice = page.getByRole('status').filter({ hasText: 'Match confirmed' })
  await expect(notice).toBeFocused()
  await notice.getByRole('button', { name: 'Undo confirmation' }).click()
  const undone = page.getByRole('status').filter({ hasText: 'Undone' })
  await expect(undone).toContainText('The match is a suggestion again')
  await expect(undone).toBeFocused()
  expect(posts).toEqual(['confirm:a', 'unconfirm:a'])
  // Both suggestions are waiting again, with the competing note back.
  await expect(cards).toHaveCount(2)
  await expect(page.locator('article[data-match-id="a"]').getByTestId('competing')).toBeVisible()
  await page.screenshot({ path: info.outputPath('undo-done.png'), fullPage: true })

  await page.getByRole('button', { name: 'Closed', exact: true }).click()
  const closed = page.getByTestId('closed-list')
  const rejected = closed.locator('article[data-match-id="r"]')
  await expect(rejected).toBeVisible()
  await expect(rejected.getByTestId('decision')).toHaveText('Rejected by an MCP client (not this screen).')
  await expect(rejected).toContainText('Via MCP')
  await expect(rejected.getByRole('button')).toHaveCount(0)
  await expect(closed.getByRole('article')).toHaveCount(1)
  await closed.getByLabel('Outcome').selectOption('reversed')
  await expect(closed.getByText('No closed matches fit these filters.')).toBeVisible()
  await closed.getByLabel('Outcome').selectOption('both')
  await expect(rejected).toBeVisible()
  await page.screenshot({ path: info.outputPath('closed-list.png'), fullPage: true })
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
      // The view switch: each button holds its own label (no clipping), no two buttons overlap, and it stays on one row
      // only when the three labels fit (at 200% text they wrap onto more rows instead of running into each other).
      const switchButtons = page.getByRole('group', { name: 'Show' }).getByRole('button')
      await expect(switchButtons).toHaveCount(3)
      const switchBoxes = []
      for (const b of await switchButtons.all()) {
        const { scroll, client } = await b.evaluate((el) => ({ scroll: el.scrollWidth, client: el.clientWidth }))
        expect(scroll, 'label fits its button').toBeLessThanOrEqual(client)
        switchBoxes.push((await b.boundingBox())!)
      }
      for (let i = 0; i < switchBoxes.length; i++) {
        for (let j = i + 1; j < switchBoxes.length; j++) {
          const [a, c] = [switchBoxes[i]!, switchBoxes[j]!]
          const overlap = a.x < c.x + c.width && c.x < a.x + a.width && a.y < c.y + c.height && c.y < a.y + a.height
          expect(overlap, 'view switch buttons do not overlap').toBe(false)
        }
      }
      if (!s.textScale) expect(new Set(switchBoxes.map((b) => b.y)).size, 'view switch on one row when it fits').toBe(1)
      await page.getByRole('button', { name: 'Reject suggestion' }).first().click()
      await expect(page.getByLabel('Reason (optional)')).toBeVisible()
      await noOverflow(page)
      await page.getByRole('button', { name: 'Cancel' }).click()
      await page.getByRole('button', { name: 'Closed', exact: true }).click()
      await expect(page.locator('article[data-match-id="r"]')).toBeVisible()
      await noOverflow(page)
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
