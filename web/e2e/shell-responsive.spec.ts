import { expect, test, type Page } from '@playwright/test'

// The shell at 320 CSS px and at 200% zoom (WCAG 1.4.10 reflow, 1.4.4 resize text). The API is stubbed in the browser,
// so these run offline against the real embedded UI without a seeded database.
const now = '2026-11-21T10:00:00Z'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const payment = (status: string, timing: string, outstanding: string) => ({
  status, derived_status: status, timing, paid_amount: eur('0.00'), outstanding_amount: eur(outstanding), overpaid_amount: eur('0.00'), shortfall_amount: eur('0.00'),
  unconverted_currencies: [], evidence_count: 0, manual_evidence_count: 0, bank_evidence_count: 0, grace_until: null, within_grace: false, override: null,
})
const occ = (id: string, name: string, due: string, status: string, timing: string) => ({
  id, commitment_id: 'c' + id, commitment_name: name, month: '2026-11', due_date: due, amount: eur('1124.19'), version: 1, payment: payment(status, timing, '1124.19'),
})

async function signedIn(page: Page) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants: ['commitments:read', 'users:read', 'tokens:read'] })))
  await page.route('**/api/v1/occurrences/summary*', (r) =>
    r.fulfill(body({
      month: '2026-11', as_of: '2026-11-21',
      items: [occ('a', 'Rent with a rather long commitment name that must wrap', '2026-11-05', 'overdue', 'overdue'), occ('b', 'Insurance', '2026-11-28', 'upcoming', 'upcoming')],
      totals: [{ currency: 'EUR', due: eur('2248.38'), paid: eur('0.00'), outstanding: eur('2248.38'), overdue: eur('1124.19'), overpaid: eur('0.00'), occurrence_count: 2, overdue_count: 1, unconverted_evidence_count: 0 }],
      freshness: { computed_at: now, occurrences_changed_at: null, bank: { available: false, note: '' } },
    })))
}

const noOverflow = async (page: Page) => {
  const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
  expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
}
const insideViewport = async (page: Page, selector: ReturnType<Page['locator']>) => {
  const box = (await selector.boundingBox())!
  const width = page.viewportSize()!.width
  expect(box.x).toBeGreaterThanOrEqual(0)
  expect(box.x + box.width).toBeLessThanOrEqual(width)
}

const sizes = [
  { name: '320 CSS px wide', viewport: { width: 320, height: 640 }, textScale: false },
  { name: '200% page zoom (1280 px at 200% is 640 CSS px)', viewport: { width: 640, height: 360 }, textScale: false },
  { name: '320 CSS px wide with 200% text size', viewport: { width: 320, height: 640 }, textScale: true },
]

for (const s of sizes) {
  test.describe(s.name, () => {
    test.use({ viewport: s.viewport })
    const zoomText = (page: Page) => (s.textScale ? page.addStyleTag({ content: 'html { font-size: 200% }' }) : Promise.resolve())

    test('signed-out sign-in page: brand and theme only, nothing overflows', async ({ page }) => {
      await page.goto('/sign-in')
      await zoomText(page)
      await expect(page.getByRole('heading', { level: 1, name: 'Sign in' })).toBeVisible()
      await expect(page.getByRole('navigation')).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Account' })).toHaveCount(0)
      await insideViewport(page, page.getByLabel('Theme'))
      await noOverflow(page)
    })

    test('signed-in month page: header, menu and content fit', async ({ page }) => {
      await signedIn(page)
      await page.goto('/month?month=2026-11')
      await zoomText(page)
      await expect(page.getByRole('heading', { level: 1, name: 'November 2026' })).toBeVisible()
      await expect(page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Monthly view' })).toBeVisible()
      await noOverflow(page)
      const account = page.getByRole('button', { name: 'Account' })
      await insideViewport(page, account)
      await insideViewport(page, page.getByLabel('Theme'))
      await insideViewport(page, page.getByRole('button', { name: 'Record a payment' }))

      // The menu opens by keyboard, fits, lists both groups, and Escape returns to the button.
      await account.focus()
      await page.keyboard.press('Enter')
      await expect(account).toHaveAttribute('aria-expanded', 'true')
      const menu = page.locator('#' + (await account.getAttribute('aria-controls')))
      await expect(menu).toBeVisible()
      await insideViewport(page, menu)
      await expect(menu.getByText('Administration')).toBeVisible()
      await expect(menu.getByRole('link', { name: 'All API tokens' })).toBeVisible()
      await expect(menu.getByRole('button', { name: 'Sign out' })).toBeVisible()
      await noOverflow(page)
      await page.keyboard.press('Tab')
      await expect(menu.getByRole('link', { name: 'Security' })).toBeFocused()
      await page.keyboard.press('Escape')
      await expect(account).toBeFocused()
      await expect(menu).toBeHidden()
    })

    test('the in-page content reflows: rows and totals stay inside the viewport', async ({ page }) => {
      await signedIn(page)
      await page.goto('/month?month=2026-11')
      await zoomText(page)
      await expect(page.getByRole('heading', { name: 'Occurrences' })).toBeVisible()
      await insideViewport(page, page.getByRole('heading', { name: 'Occurrences' }))
      for (const row of await page.getByRole('listitem').all()) await insideViewport(page, row)
      await noOverflow(page)
    })
  })
}
