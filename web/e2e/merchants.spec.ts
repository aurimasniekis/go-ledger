import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

// The merchant screens (E3) in a real browser against the embedded UI, with the API stubbed in the browser: the list, one
// record with its aliases, the merge preview that says a merge cannot be undone in place, and no sideways scrolling at 390 px.
const now = '2026-11-21T10:00:00Z'
const alias = (id: string, name: string) => ({ id, name, key: name.toLowerCase(), origin: 'user', created_at: now })
const rec = (id: string, name: string, aliases: string[]) => ({
  id, display_name: name, aliases: aliases.map((a, i) => alias(id + 'a' + i, a)), archived: false, archived_at: null, merged_into: null, version: 1, created_at: now, updated_at: now,
})
const LIDL = rec('m1', 'Lidl', ['Lidl', 'Lidl Vilnius supermarket with a deliberately long alias name to test wrapping on a narrow screen'])
const MAXIMA = rec('m2', 'Maxima', ['Maxima'])

async function stub(page: Page) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'merchants:read', 'merchants:write'] })))
  await page.route('**/api/v1/merchants*', (r) => r.fulfill(body({ items: [LIDL, MAXIMA], next_cursor: null })))
  await page.route('**/api/v1/merchants/m1', (r) => r.fulfill(body(LIDL)))
}

test('the merchant list links to a record with its aliases', async ({ page }) => {
  await stub(page)
  await page.goto('/merchants')
  await expect(page.getByRole('heading', { level: 1, name: 'Merchants' })).toBeVisible()
  await page.getByRole('link', { name: 'Lidl' }).click()
  await expect(page).toHaveURL(/\/merchants\/m1$/)
  await expect(page.getByRole('heading', { level: 1, name: /Lidl/ })).toBeVisible()
  await expect(page.getByRole('list', { name: 'aliases' })).toContainText('Lidl Vilnius supermarket')
})

test('the merge preview says what moves and that it cannot be undone in place', async ({ page }) => {
  await stub(page)
  await page.goto('/merchants/m1')
  await page.getByRole('button', { name: 'Merge another merchant into this one' }).click()
  const dlg = page.getByRole('dialog')
  await expect(dlg.getByRole('button', { name: 'Merge', exact: true })).toBeDisabled()
  await dlg.getByLabel('Merchant to merge into this one').selectOption('m2')
  await expect(dlg).toContainText('Maxima will be merged into Lidl, which remains.')
  await expect(dlg).toContainText('A merge cannot be undone in place.')
  await dlg.getByLabel('I understand this merge cannot be undone in place.').check()
  await expect(dlg.getByRole('button', { name: 'Merge', exact: true })).toBeEnabled()
})

test('a merchant record can be organised by its id', async ({ page }) => {
  await stub(page)
  await page.route('**/api/v1/me', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'merchants:read', 'merchants:write', 'organisation:read', 'organisation:write'] }) }))
  await page.route('**/api/v1/organisation/merchant/m1', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ object_type: 'merchant', object_id: 'm1', category: null, labels: [], removed_labels: [], buckets: [] }) }))
  await page.route('**/api/v1/categories*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next_cursor: null }) }))
  await page.route('**/api/v1/labels*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next_cursor: null }) }))
  await page.route('**/api/v1/buckets*', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [], next_cursor: null }) }))
  await page.goto('/merchants/m1')
  await page.getByRole('button', { name: 'Organise Lidl' }).click()
  const panel = page.getByRole('group', { name: 'Organisation of Lidl' })
  await expect(panel).toContainText('No category yet')
  await expect(panel.getByRole('heading', { name: 'Buckets' })).toHaveCount(0)
})

for (const path of ['/merchants', '/merchants/m1']) {
  test('at 390 px nothing scrolls sideways: ' + path, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await stub(page)
    await page.goto(path)
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()
    await expect(page.getByText('Lidl').first()).toBeVisible()
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
    expect(overflow).toBeLessThanOrEqual(0)
  })
}
