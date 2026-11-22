import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'

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

async function signedIn(page: Page, grants = ['commitments:read', 'users:read', 'tokens:read']) {
  const body = (json: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(json) })
  await page.route('**/api/v1/auth/session', (r) =>
    r.fulfill(body({ user_id: 'u1', csrf_token: 'x', recent_auth_until: now, two_factor: { enabled: false, required: false, enrollment_required: false },
      session: { id: 's1', current: true, created_at: now, last_seen_at: now, authenticated_at: now, idle_expires_at: now, expires_at: now, user_agent: 'e2e', address: '192.0.2.1' } })))
  await page.route('**/api/v1/me', (r) =>
    r.fulfill(body({ kind: 'user', user_id: 'u1', token_id: null, role: 'administrator', grants })))
  await page.route('**/api/v1/occurrences/summary*', (r) =>
    r.fulfill(body({
      month: '2026-11', as_of: '2026-11-21',
      items: [occ('a', 'Rent with a rather long commitment name that must wrap', '2026-11-05', 'overdue', 'overdue'), occ('b', 'Insurance', '2026-11-28', 'upcoming', 'upcoming')],
      totals: [{ currency: 'EUR', due: eur('2248.38'), paid: eur('0.00'), outstanding: eur('2248.38'), overdue: eur('1124.19'), overpaid: eur('0.00'), occurrence_count: 2, overdue_count: 1, unconverted_evidence_count: 0 }],
      freshness: { computed_at: now, occurrences_changed_at: null, bank: { available: false, note: '' } },
    })))
}

// SHELL_SHOTS_DIR, when set, receives screenshots for reviewers.
const shot = async (page: Page, name: string) => {
  const dir = process.env.SHELL_SHOTS_DIR
  if (dir) await page.screenshot({ path: dir + '/' + name + '.png', fullPage: true })
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

// Each total amount is one line, never broken inside the number (the totals list once broke "1436.67" into "1436." / "67").
async function amountsOnOneLine(page: Page) {
  await expect(page.getByRole('heading', { name: 'Recorded commitments, by currency' })).toBeVisible()
  const amounts = page.getByRole('listitem').filter({ has: page.getByRole('heading', { name: 'EUR' }) }).locator('dd[data-total-amount]')
  const count = await amounts.count()
  expect(count).toBeGreaterThanOrEqual(4)
  for (let n = 0; n < count; n++) {
    const dd = amounts.nth(n)
    // The visible text only: the screen-reader copy is visually hidden and positioned apart.
    const lines = await dd.locator('[data-amount]').evaluate((el) => {
      const range = document.createRange()
      range.selectNodeContents(el)
      return new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size
    })
    expect(lines, 'amount on one line: ' + (await dd.textContent())).toBe(1)
    await insideViewport(page, dd)
  }
}

// An ISO date is one line too: it must not break at a hyphen ("2026-11-" / "05"), in rows, the freshness line or the notice.
async function datesOnOneLine(page: Page) {
  const times = page.locator('time')
  await expect(times.first()).toBeVisible()
  const count = await times.count()
  expect(count).toBeGreaterThanOrEqual(3)
  for (let n = 0; n < count; n++) {
    const t = times.nth(n)
    const text = (await t.textContent()) ?? ''
    if (!/^\d{4}-\d{2}-\d{2}/.test(text)) continue
    const lines = await t.evaluate((el) => {
      const range = document.createRange()
      range.selectNodeContents(el)
      return new Set([...range.getClientRects()].map((r) => Math.round(r.top))).size
    })
    expect(lines, 'date on one line: ' + text).toBe(1)
  }
}

// The number of rows the header's links and controls take: items on one row are centred on the same line (their heights
// differ), so count the distinct centres, 8 px apart or more.
const headerRows = (page: Page) =>
  page.getByRole('banner').evaluate((h) => {
    const items = [...h.querySelectorAll('a, button, select')].filter((el) => (el as HTMLElement).offsetParent !== null)
    const centres = items.map((el) => { const b = el.getBoundingClientRect(); return b.top + b.height / 2 }).sort((a, b) => a - b)
    return centres.reduce((n, c, i) => (i === 0 || c - centres[i - 1]! >= 8 ? n + 1 : n), 0)
  })

// Where each header item sits, for the failure message when a row count is off (text, left, width, top; fonts differ per machine).
const headerLayout = (page: Page) =>
  page.getByRole('banner').evaluate((h) =>
    JSON.stringify([...h.querySelectorAll('a, button, select')].filter((el) => (el as HTMLElement).offsetParent !== null).map((el) => {
      const b = el.getBoundingClientRect()
      return [(el.textContent ?? '').slice(0, 16), Math.round(b.left), Math.round(b.width), Math.round(b.top)]
    })) + ' font=' + getComputedStyle(h).fontFamily + ' ' + getComputedStyle(h).fontSize)

// Below 1024 px the finance pages sit behind a "Menu" disclosure (ADR-0063): the header stays short, the button and every
// link in the opened list are at least 44 px tall, and the keyboard and Escape work.
test.describe('390 CSS px wide, an administrator who can read everything', () => {
  test.use({ viewport: { width: 390, height: 844 } })
  test('the Menu is a 44 px disclosure whose links are 44 px tall and keep their full names', async ({ page }) => {
    await signedIn(page, [...allGrants])
    await page.goto('/month?month=2026-11')
    const menu = page.getByRole('button', { name: 'Menu' })
    const nav = page.getByRole('navigation', { name: 'Primary' })
    await expect(menu).toBeVisible()
    await expect(nav).toBeHidden()
    expect((await menu.boundingBox())!.height).toBeGreaterThanOrEqual(44)
    expect((await page.getByRole('button', { name: 'Account' }).boundingBox())!.height).toBeGreaterThanOrEqual(44)
    expect(await headerRows(page), 'rows in the header, menu closed').toBeLessThanOrEqual(2)
    await menu.focus()
    await page.keyboard.press('Enter')
    await expect(menu).toHaveAttribute('aria-expanded', 'true')
    for (const name of ['Monthly view', 'Outlook', 'Payment matches', 'Commitments', 'Organisation', 'Merchants', 'Bank connections']) {
      const link = nav.getByRole('link', { name, exact: true })
      await expect(link).toBeVisible()
      expect((await link.boundingBox())!.height, name + ' is at least 44 px tall').toBeGreaterThanOrEqual(44)
      await insideViewport(page, link)
    }
    await shot(page, 'header-390-menu-open')
    await noOverflow(page)
    await page.keyboard.press('Tab')
    await expect(nav.getByRole('link', { name: 'Monthly view' })).toBeFocused()
    await page.keyboard.press('Escape')
    await expect(menu).toBeFocused()
    await expect(nav).toBeHidden()
  })
})

// From lg (1024 px) up the finance pages are links in the header row.
test.describe('1280 CSS px wide', () => {
  test.use({ viewport: { width: 1280, height: 800 } })
  test('the links are in the header and there is no Menu button', async ({ page }) => {
    await signedIn(page, [...allGrants])
    await page.goto('/month?month=2026-11')
    await expect(page.getByRole('button', { name: 'Menu' })).toBeHidden()
    const nav = page.getByRole('navigation', { name: 'Primary' })
    for (const name of ['Monthly view', 'Merchants', 'Commitments']) await expect(nav.getByRole('link', { name, exact: true })).toBeVisible()
    expect(await headerRows(page), 'rows in the header').toBeLessThanOrEqual(2)
  })
})

// The header is at most two rows from 320 to 1280 CSS px, with and without connections:read. The administrator has the
// longest link row (the 320, 340, 360 and 390 px cases are the phone widths it was checked at first).
// "Monthly" keeps its full word on a phone (WCAG 2.5.3: the visible label is part of the accessible name "Monthly view").
const allGrants = ['commitments:read', 'users:read', 'tokens:read', 'connections:read', 'merchants:read', 'organisation:read']
const adminGrants = allGrants
const memberGrants = ['commitments:read', 'users:read', 'tokens:read']
// Not every width: a sweep of all 961 widths outran the test's timeout on a loaded machine. The widths are every 16 CSS px
// from 320 to 1280, plus each layout breakpoint (Tailwind's sm 640, md 768, lg 1024 and xl 1280, and the 830 px edge of the
// 640 to 830 range above) with the widths one px either side, so each boundary is checked on both sides. SHELL_RESPONSIVE_FULL=1
// checks every width instead, for a slower, exhaustive run.
const layoutBreakpoints = [640, 768, 830, 1024, 1280]
const headerWidths = (() => {
  const full = process.env.SHELL_RESPONSIVE_FULL === '1'
  const widths = new Set<number>()
  for (let w = 320; w <= 1280; w++) if (full || (w - 320) % 16 === 0) widths.add(w)
  for (const b of layoutBreakpoints) for (const w of [b - 1, b, b + 1]) if (w >= 320 && w <= 1280) widths.add(w)
  return [...widths].sort((a, b) => a - b)
})()
for (const [label, grants] of [['an administrator with connections:read', adminGrants], ['without connections:read', memberGrants]] as const) {
  test.describe('320 to 1280 CSS px wide, ' + label, () => {
    test.use({ viewport: { width: 320, height: 640 } })
    test('the header is at most two rows and nothing overflows', async ({ page }) => {
      test.setTimeout(120_000)
      await signedIn(page, [...grants])
      await page.goto('/month?month=2026-11')
      for (const width of headerWidths) {
        await page.setViewportSize({ width, height: 640 })
        const rows = await headerRows(page)
        expect(rows, `rows in the header at ${width} px: ${rows > 2 ? await headerLayout(page) : ''}`).toBeLessThanOrEqual(2)
        await noOverflow(page)
      }
    })
  })
}

// One extra phone width (375), kept separate so the browser is launched for as few extra pages as possible.
test.describe('375 CSS px wide', () => {
  test.use({ viewport: { width: 375, height: 667 } })
  test('each total amount stays on one line', async ({ page }) => {
    await signedIn(page)
    await page.goto('/month?month=2026-11')
    await amountsOnOneLine(page)
    await datesOnOneLine(page)
  })
})

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
      await expect(page.getByRole('button', { name: 'Menu' })).toBeVisible()
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
      await amountsOnOneLine(page)
      await datesOnOneLine(page)
      await insideViewport(page, page.getByRole('heading', { name: 'Occurrences' }))
      for (const row of await page.getByRole('listitem').all()) await insideViewport(page, row)
      await noOverflow(page)
    })
  })
}
