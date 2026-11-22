import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The suggestions and alerts review (L7) in a real browser against the embedded UI, with the API stubbed in the browser
// (as merchants.spec.ts does): no model, bank or network service is involved. Nothing here needs its own server or port:
// the stub holds the state, so the shared main server on 18080 only serves the UI bundle.
const now = '2026-11-21T10:00:00Z'
const json = (b: unknown, status = 200) => ({ status, contentType: 'application/json', body: JSON.stringify(b) })
const def = (id: string, name: string, kind: string) => ({ id, kind, name, parent_id: null, archived: false, archived_at: null, version: 1, created_at: now, updated_at: now })
const sug = (id: string, over: Record<string, unknown> = {}) => ({
  id, task: 'categorisation', object_type: 'commitment', object_id: 'c1', state: 'pending',
  suggested_by: { provider: 'testprov', model: 'test-model', label: 'Test provider' }, provenance: 'suggested by testprov/test-model',
  response_id: null, decision: { choice: 'cat1', choices: [], confidence: 'medium' }, auto_apply: false, reversal: null,
  created_at: now, decided_at: null, version: 1, ...over,
})

async function stub(page: Page) {
  const sugs = [sug('s1'), sug('s2', { object_id: 'c2', decision: { choice: 'cat2', choices: [], confidence: null } })]
  const alerts = [{ id: 'a1', kind: 'consent_expiring', object_type: 'bank_connection', object_id: 'bc1', facts: { days_left: '5' }, message: 'The consent for this bank connection ends in 5 days.', model_text: null, state: 'open', created_at: now, closed_at: null, version: 1 }]
  const posts: string[] = []
  const conflict = new Set<string>()
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(json({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(json({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'decisions:read', 'decisions:write', 'organisation:read', 'organisation:write'] })))
  await page.route('**/api/v1/categories*', (r) => r.fulfill(json({ items: [def('cat1', 'Groceries', 'category'), def('cat2', 'Home', 'category')], next_cursor: null })))
  await page.route('**/api/v1/labels*', (r) => r.fulfill(json({ items: [], next_cursor: null })))
  await page.route('**/api/v1/commitments/*', (r) => r.fulfill(json({ id: 'c1', name: 'Weekly shop' })))
  await page.route('**/api/v1/suggestions?*', (r) => {
    const st = new URL(r.request().url()).searchParams.getAll('state')
    return r.fulfill(json({ items: sugs.filter((s) => st.includes(s.state as string)), next_cursor: null }))
  })
  await page.route('**/api/v1/suggestions/*/*', (r) => {
    const [, id = '', action = ''] = new URL(r.request().url()).pathname.match(/suggestions\/([^/]+)\/([^/]+)$/)!
    posts.push(id + ':' + action)
    const s = sugs.find((x) => x.id === id)!
    if (conflict.has(id)) {
      s.state = 'accepted'
      conflict.delete(id)
      return r.fulfill(json({ code: 'suggestion_not_pending', message: 'already decided', request_id: 'req_e2e' }, 409))
    }
    s.state = action === 'accept' ? 'accepted' : 'rejected'
    return r.fulfill(json(s))
  })
  await page.route('**/api/v1/alerts?*', (r) => {
    const st = new URL(r.request().url()).searchParams.getAll('state')
    return r.fulfill(json({ items: alerts.filter((a) => st.includes(a.state)), next_cursor: null }))
  })
  await page.route('**/api/v1/alerts/*/dismiss', (r) => {
    alerts[0]!.state = 'dismissed'
    posts.push('a1:dismiss')
    return r.fulfill(json(alerts[0]))
  })
  return { posts, conflict }
}

async function noOverflow(page: Page) {
  const w = await page.evaluate(() => ({ doc: document.documentElement.scrollWidth, view: document.documentElement.clientWidth }))
  expect(w.doc, 'no sideways scrolling').toBeLessThanOrEqual(w.view)
}

test('review: provenance is visible, accept and reject by keyboard-reachable buttons, alerts dismiss', async ({ page }) => {
  const { posts } = await stub(page)
  await page.goto('/suggestions')
  await expect(page.getByRole('heading', { level: 1, name: 'Suggestions and alerts' })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Suggestions', exact: true }).first()).toBeAttached()
  const first = page.getByRole('article').first()
  await expect(first).toContainText('suggested by testprov/test-model')
  await expect(first).toContainText('Groceries')
  await first.getByRole('button', { name: 'Accept' }).click()
  await expect(page.getByRole('status').filter({ hasText: 'Accepted: category Groceries' })).toBeVisible()
  await expect(page.getByRole('article')).toHaveCount(1)
  await page.getByRole('article').getByRole('button', { name: 'Reject' }).click()
  await expect(page.getByText(/Rejected: nothing was assigned/)).toBeVisible()
  await expect(page.getByText(/No suggestions are waiting/)).toBeVisible()
  await page.getByRole('button', { name: 'Decided' }).click()
  await expect(page.getByRole('article')).toHaveCount(2)
  await page.getByRole('button', { name: 'Open alerts' }).click()
  await expect(page.getByRole('article')).toContainText('ends in 5 days')
  await page.getByRole('button', { name: 'Dismiss', exact: true }).last().click()
  await expect(page.getByText(/Dismissed: "Bank consent expiring"/)).toBeVisible()
  expect(posts).toEqual(['s1:accept', 's2:reject', 'a1:dismiss'])
})

test('a refused accept is explained on the page and keeps focus after the card leaves', async ({ page }) => {
  const { conflict } = await stub(page)
  conflict.add('s1')
  await page.goto('/suggestions')
  await page.getByRole('article').first().getByRole('button', { name: 'Accept' }).click()
  const note = page.getByRole('alert').filter({ hasText: 'already decided' })
  await expect(note).toBeVisible()
  await expect(page.getByRole('article')).toHaveCount(1)
  await expect(note).toBeFocused()
})

test('Correct moves focus into the form and Cancel returns it', async ({ page }) => {
  await stub(page)
  await page.goto('/suggestions')
  await page.getByRole('button', { name: 'Correct…' }).first().click()
  await expect(page.getByLabel('Your category')).toBeFocused()
  await page.getByRole('button', { name: 'Cancel' }).click()
  await expect(page.getByRole('button', { name: 'Correct…' }).first()).toBeFocused()
})

for (const s of [
  { name: '320 CSS px wide', width: 320, height: 640 },
  { name: '390 CSS px wide', width: 390, height: 844 },
]) {
  test.describe('suggestions at ' + s.name, () => {
    test.use({ viewport: { width: s.width, height: s.height } })
    test('the list, the correct form and the alerts reflow without sideways scrolling', async ({ page }) => {
      await stub(page)
      await page.goto('/suggestions')
      await expect(page.getByRole('article')).toHaveCount(2)
      await noOverflow(page)
      await page.getByRole('button', { name: 'Correct…' }).first().click()
      await expect(page.getByLabel('Your category')).toBeVisible()
      await noOverflow(page)
      await page.getByRole('button', { name: 'Open alerts' }).click()
      await expect(page.getByRole('article')).toHaveCount(1)
      await noOverflow(page)
      for (const b of await page.getByRole('button').all()) {
        const box = await b.boundingBox()
        if (!box) continue
        expect(box.x).toBeGreaterThanOrEqual(0)
        expect(box.x + box.width).toBeLessThanOrEqual(s.width)
      }
    })
  })
}
