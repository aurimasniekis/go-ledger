import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { BANK_PORT, JOURNEY_PASSWORD, JOURNEY_USER, LEDGER_PORT, journeyDates } from './journey-data.mjs'

// The offline main journey (U3): the real ledger binary with its embedded UI, a throwaway database and a simulated
// Enable Banking API (journey-server.mjs). Nothing is stubbed in the browser, nothing reaches a live bank, rate
// service or model. One browser context walks the whole journey, because the application state is the point:
// import accounts, balances and transactions; record commitments; read the monthly view; confirm a suggested match
// and correct a payment; reload and find the same statuses and totals; read the outlook with its balance date; then
// see the failed-refresh, offline and withdrawn-consent states. docs/decisions/0047 lists what it cannot cover.
// The browser and the binary both run in UTC, so the dates the journey computes are the dates the screens show.
test.use({ baseURL: `http://127.0.0.1:${LEDGER_PORT}`, timezoneId: 'UTC' })

const d = journeyDates()
const bank = (name: string, on: boolean) => fetch(`http://127.0.0.1:${BANK_PORT}/__control/${name}?on=${on}`, { method: 'POST' })

// Commitments have no screen yet, and a matching run has no button on the month view: both go through the
// Management API as the signed-in session, with its CSRF token.
async function post(page: Page, path: string, data?: Record<string, unknown>) {
  const session = await (await page.request.get('/api/v1/auth/session')).json()
  return page.request.post(path, { data, headers: { 'X-CSRF-Token': session.csrf_token } })
}
async function createCommitment(page: Page, body: Record<string, unknown>) {
  const res = await post(page, '/api/v1/commitments', body)
  expect(res.status(), await res.text()).toBe(201)
}
// The recurring ones end in the previous month, so every figure below is the same on any day of the year.
const monthly = (name: string, kind: string, amount: string, day: number) => ({
  name, kind, amount: { amount, currency: 'EUR' }, schedule: { version: 1, frequency: 'monthly', interval: 1, day }, start_date: d.day(1), end_date: d.day(28),
})

test('import, commitments, monthly view, match, correction, reload, outlook and degraded states', async ({ page, context }) => {
  test.setTimeout(120_000)
  const occurrence = (name: string) => page.getByRole('listitem').filter({ has: page.getByText(name, { exact: true }) })
  const totals = (currency: string) => page.getByRole('heading', { level: 3, name: currency }).locator('..')
  const value = (scope: ReturnType<typeof totals>, label: string) => scope.getByText(label, { exact: true }).locator('xpath=following-sibling::dd[1]')
  const monthView = async () => {
    await page.goto('/month?month=' + d.prevMonth)
    await expect(page.getByTestId('status-summary')).toBeVisible()
  }
  const connection = () => page.getByRole('article', { name: 'Example Bank' })
  const balance = /1\s250\.40\s+EUR/

  await test.step('sign in', async () => {
    await page.goto('/')
    await page.getByLabel('Username').fill(JOURNEY_USER)
    await page.getByLabel('Password').fill(JOURNEY_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page).toHaveURL(/\/month/)
  })

  await test.step('connect the simulated bank and import accounts, balances and transactions', async () => {
    await page.goto('/connections')
    await page.getByRole('button', { name: 'Connect a bank' }).first().click()
    const dialog = page.getByRole('dialog', { name: 'Connect a bank' })
    await dialog.getByLabel('Country of the bank').fill('FI')
    await page.keyboard.press('Enter')
    await dialog.getByLabel('Bank', { exact: true }).selectOption('Example Bank')
    await dialog.getByRole('button', { name: 'Continue to your bank' }).click()
    // The simulated bank approves at once and sends the browser back with a code, which is used once and removed.
    await expect(page.getByRole('status').filter({ hasText: 'Example Bank is connected' })).toBeVisible()
    expect(page.url()).not.toContain('code=')
    await page.getByRole('button', { name: 'Fetch accounts now' }).click()
    await expect(page.getByText('Fetched 1 account(s).')).toBeVisible()
    await page.getByRole('link', { name: 'Go to bank connections' }).first().click()
    await expect(connection().locator('[data-amount="visual"]', { hasText: balance })).toBeVisible()
    await expect(connection().getByText('Up to date')).toBeVisible()
  })

  await test.step('record commitments after the import', async () => {
    await createCommitment(page, monthly('Rent', 'service', '800.00', 2))
    await createCommitment(page, monthly('Server hosting', 'server', '20.00', 12))
    await createCommitment(page, monthly('Insurance', 'service', '100.00', 5))
    await createCommitment(page, { ...monthly('Domain renewal', 'domain', '15.00', 15), start_date: d.nextMonth + '-01', end_date: null })
  })

  await test.step('matching: the exact payment is confirmed by the engine, the ambiguous one stays a suggestion', async () => {
    const res = await post(page, '/api/v1/payment-matching/runs')
    expect(res.status()).toBe(200)
    expect(await res.json()).toMatchObject({ confirmed: 1, suggested: 2 })
  })

  await test.step('monthly view: bank-matched rent is paid, hosting waits for a person, older occurrences are not recorded', async () => {
    await monthView()
    await expect(page.getByTestId('status-summary')).toHaveText('1 paid · 2 not recorded')
    // A bank is connected and was just fetched: the month view must not claim otherwise.
    await expect(page.getByText('No bank data connected yet')).toHaveCount(0)
    await expect(page.getByTestId('month-bank-line')).toContainText('Bank data is current')
    await expect(occurrence('Rent')).toContainText('Paid')
    await expect(occurrence('Rent')).toContainText('Bank match')
    await expect(occurrence('Server hosting')).toContainText('Not recorded')
    await expect(occurrence('Server hosting').getByTestId('suggestion-waiting')).toBeVisible()
    await expect(occurrence('Insurance')).toContainText('Due before this commitment was created')
    await expect(value(totals('EUR'), 'Scheduled this month')).toContainText('920.00 EUR')
    await expect(value(totals('EUR'), 'Paid')).toContainText('800.00 EUR')
    await expect(value(totals('EUR'), 'Outstanding')).toContainText('0.00 EUR')
  })

  await test.step('review the ambiguous suggestions: confirm one transaction, reject the other', async () => {
    await page.getByRole('link', { name: /Review it before recording/ }).first().click()
    await expect(page).toHaveURL(/\/matches/)
    const cards = page.getByRole('article')
    await expect(cards).toHaveCount(2)
    await expect(cards.first().getByTestId('competing')).toContainText('2 suggestions compete for this occurrence')
    await cards.first().getByRole('button', { name: 'Confirm match' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Match confirmed' })).toContainText('Server hosting, ' + d.prevMonthName)
    await expect(cards).toHaveCount(1)
    await cards.first().getByRole('button', { name: 'Reject suggestion' }).click()
    await cards.first().getByRole('button', { name: 'Reject now' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Suggestion rejected' })).toBeVisible()
    await expect(page.getByRole('article')).toHaveCount(0)
  })

  await test.step('correct the rent payment: only part of the transaction pays the occurrence', async () => {
    await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
    const rent = page.getByRole('article').filter({ hasText: 'Rent' })
    await expect(rent.getByTestId('decision')).toContainText('Confirmed automatically by the matching engine')
    await rent.getByRole('button', { name: 'Correct amount' }).click()
    await rent.getByLabel(/Allocated amount/).fill('500.00')
    await rent.getByLabel('Reason (optional)').fill('landlord credited 300.00 back')
    await rent.getByRole('button', { name: 'Save correction' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Amount corrected' })).toBeVisible()
    await expect(rent).toContainText('500.00 EUR')
  })

  const expectCorrectedMonth = async () => {
    await expect(page.getByTestId('status-summary')).toHaveText('1 partly paid · 1 paid · 1 not recorded')
    await expect(occurrence('Rent')).toContainText(/Paid 500\.00\sEUR.*outstanding 300\.00\sEUR/s)
    await expect(occurrence('Server hosting')).toContainText('Paid')
    await expect(occurrence('Server hosting').getByTestId('suggestion-waiting')).toHaveCount(0)
    await expect(value(totals('EUR'), 'Paid')).toContainText('520.00 EUR')
    // A not-recorded remainder is left out of outstanding (ADR-0042).
    await expect(value(totals('EUR'), 'Outstanding')).toContainText('0.00 EUR')
  }
  await test.step('monthly view and a reload agree: statuses and totals after the correction', async () => {
    await monthView()
    await expectCorrectedMonth()
    await page.reload()
    await expect(page.getByTestId('status-summary')).toBeVisible()
    await expectCorrectedMonth()
  })

  await test.step('organise: set a category on an occurrence and on a commitment with none this month, reload, and the origin says it is the person\'s', async () => {
    const res = await post(page, '/api/v1/categories', { name: 'Housing' })
    expect(res.status(), await res.text()).toBe(201)
    await monthView()
    await occurrence('Rent').getByRole('button', { name: 'Organise Rent' }).click()
    const panel = page.getByRole('group', { name: 'Organisation of Rent' })
    await expect(panel.getByText('No category yet')).toBeVisible()
    await panel.getByLabel('Category for Rent').selectOption({ label: 'Housing' })
    await panel.getByRole('button', { name: 'Set category' }).click()
    await expect(panel.getByText('Saved: Housing is your choice for Rent.')).toBeVisible()
    await page.reload()
    await expect(page.getByTestId('status-summary')).toBeVisible()
    await occurrence('Rent').getByRole('button', { name: 'Organise Rent' }).click()
    const again = page.getByRole('group', { name: 'Organisation of Rent' })
    await expect(again.locator('strong', { hasText: 'Housing' })).toBeVisible()
    await expect(again.getByText(/Your choice/)).toBeVisible()
    // The commitment that starts next month has no occurrence here, and can still be organised from its own list.
    await page.goto('/commitments')
    await page.getByRole('button', { name: 'Organise Domain renewal' }).click()
    await expect(page.getByRole('group', { name: 'Organisation of Domain renewal' }).getByText('No category yet')).toBeVisible()
  })

  await test.step('outlook: balance with its date, payments recorded after it, the commitment still due', async () => {
    await page.goto('/outlook?through=' + d.nextMonth)
    const card = page.getByTestId('outlook-EUR')
    await expect(page.getByRole('heading', { name: 'Outlook against recorded commitments' })).toBeVisible()
    await expect(card.getByText('Bank balance, as of ' + d.day(1))).toBeVisible()
    await expect(card).toContainText(/1\s250\.40\sEUR/)
    await expect(value(card, 'Recorded commitments still due')).toContainText('15.00 EUR')
    await expect(value(card, 'of which upcoming')).toContainText('15.00 EUR')
    await expect(value(card, 'of which overdue (past original due date)')).toContainText('0.00 EUR')
    await expect(card.getByText('Balance minus recorded commitments still due')).toBeVisible()
    await expect(card).toContainText(/1\s235\.40\sEUR/) // 1 250.40 balance minus 15.00 still due
    // 520.00 corrected rent + 20.00 hosting were recorded as paid after the balance's date.
    await expect(page.getByTestId('outlook-after-EUR')).toContainText('2 payments')
    await expect(page.getByTestId('outlook-after-EUR')).toContainText('already off "still due" but may not be in the bank balance yet')
    // Rent, hosting and insurance of the previous month were due before they were recorded: never counted as overdue.
    await expect(page.getByTestId('outlook-not-recorded')).toContainText('2 occurrences were due before the commitments they belong to were created')
  })

  await test.step('a failed refresh: local data stays, marked as such, and the outlook says the balance is not fresh', async () => {
    await bank('outage', true)
    await page.goto('/connections')
    await connection().getByRole('button', { name: 'Refresh now' }).click()
    // The adapter retries a failing bank with a backoff before it gives up, so this takes several seconds.
    await expect(connection().getByText('Temporary problem').first()).toBeVisible({ timeout: 30_000 })
    await expect(connection().getByRole('button', { name: /Reconnect/ })).toHaveCount(0)
    await expect(connection().locator('[data-amount="visual"]', { hasText: balance })).toBeVisible()
    await page.goto('/outlook?through=' + d.nextMonth)
    await expect(page.getByTestId('outlook-EUR')).toContainText('1 of 1 balance not fresh: 1 last refresh failed')
    await expect(page.getByText('Last refresh failed', { exact: true })).toBeVisible()
    await expect(page.getByTestId('outlook-EUR')).toContainText(balance) // the last balance is kept, with its date
    await monthView()
    await expectCorrectedMonth() // local data is untouched by the outage
    await expect(page.getByTestId('month-bank-line')).toContainText('latest bank fetch failed')
    await bank('outage', false)
  })

  await test.step('offline browser: the monthly view says so and recording a payment is off', async () => {
    await context.setOffline(true)
    await expect(page.getByText('Offline', { exact: true })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Record a payment' })).toBeDisabled()
    await expectCorrectedMonth() // what was fetched stays on screen
    await context.setOffline(false)
    await expect(page.getByRole('button', { name: 'Record a payment' })).toBeEnabled()
  })

  await test.step('a withdrawn consent needs a reconnect; reconnecting makes the connection usable again', async () => {
    await bank('revoked', true)
    await page.goto('/connections')
    await connection().getByRole('button', { name: /Refresh now|Try again/ }).click()
    await expect(connection().getByText('Reconnect needed').first()).toBeVisible({ timeout: 30_000 })
    await expect(connection().getByText('Retrying will not help')).toBeVisible()
    await expect(connection().locator('[data-amount="visual"]', { hasText: balance })).toBeVisible()
    await bank('revoked', false)
    await connection().getByRole('button', { name: 'Reconnect bank' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Example Bank is connected' })).toBeVisible()
    await page.goto('/connections')
    await expect(connection().getByText('Reconnected, not refreshed yet')).toBeVisible()
    await expect(connection().getByText('Reconnect needed')).toHaveCount(0)
    await expect(connection().getByText('Up to date')).toHaveCount(0)
    await expect(connection().getByText(/Data last refreshed/)).toBeVisible()
    await expect(connection().getByRole('button', { name: /Reconnect/ })).toHaveCount(0)
    await connection().getByRole('button', { name: 'Refresh now' }).click()
    await expect(connection().getByText('Up to date')).toBeVisible({ timeout: 30_000 })
    await expect(connection().getByText('Reconnected, not refreshed yet')).toHaveCount(0)
  })
})
