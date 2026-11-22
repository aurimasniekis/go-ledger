import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The organisation screen (O2) in a real browser against the embedded UI, with the API stubbed in the browser: the
// two-level category tree, labels and buckets, the Organise control of an occurrence, and no sideways scrolling at 390 px.
const now = '2026-11-21T10:00:00Z'
const def = (kind: string, id: string, name: string, parent: string | null = null) => ({
  id, kind, name, parent_id: parent, archived: false, archived_at: null, version: 1, created_at: now, updated_at: now,
})

async function stub(page: Page) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  const list = (items: unknown[]) => body({ items, next_cursor: null })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'organisation:read', 'organisation:write'] })))
  await page.route('**/api/v1/categories*', (r) => r.fulfill(list([def('category', 'c1', 'Food'), def('category', 'c2', 'Groceries and everyday household supplies from the neighbourhood shop', 'c1')])))
  await page.route('**/api/v1/labels*', (r) => r.fulfill(list([def('label', 'l1', 'Tax')])))
  await page.route('**/api/v1/buckets*', (r) => r.fulfill(list([def('bucket', 'b1', 'Household')])))
}

test('the organisation screen shows categories as a two-level tree, labels and buckets', async ({ page }) => {
  await stub(page)
  await page.goto('/organisation')
  await expect(page.getByRole('heading', { level: 1, name: 'Categories, labels and buckets' })).toBeVisible()
  const cats = page.getByRole('list', { name: 'categories' })
  await expect(cats).toContainText('Food')
  await expect(cats).toContainText('Groceries and everyday')
  await expect(page.getByRole('list', { name: 'labels' })).toContainText('Tax')
  await expect(page.getByRole('list', { name: 'buckets' })).toContainText('Household')
  await expect(page.getByText('holds no money and allocates none')).toBeVisible()
})

test('at 390 px nothing scrolls sideways', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 })
  await stub(page)
  await page.goto('/organisation')
  await expect(page.getByRole('list', { name: 'categories' })).toBeVisible()
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
})
