import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { DEFERRAL_LEDGER_PORT, JOURNEY_PASSWORD, JOURNEY_USER, deferralDates } from './journey-data.mjs'

// The deferral journey (DF3): the real ledger binary with its embedded UI and a simulated bank (journey-server.mjs, deferral
// scenario). One commitment of 50.00 EUR falls due on the 20th of next month. The journey defers it within the month, carries it
// to the month after, undoes that, plans it again, imports again and lets automatic matching pay it from a bank payment dated on
// the planned date during that import. Nothing is stubbed and nothing reaches a live bank. Statuses are read as words, never as colours.
// Next month is used because an occurrence is overdue-and-tracked only if it fell due after its commitment was created (ADR-0047),
// so a past one cannot be planned with a real clock; the deferral rules around overdue are covered by the Go and component tests.
test.use({ baseURL: `http://127.0.0.1:${DEFERRAL_LEDGER_PORT}`, timezoneId: 'UTC' })

const d = deferralDates()

async function post(page: Page, path: string, data?: Record<string, unknown>) {
  const session = await (await page.request.get('/api/v1/auth/session')).json()
  return page.request.post(path, { data, headers: { 'X-CSRF-Token': session.csrf_token } })
}

test('defer within a month, carry to the next, undo, re-import and match', async ({ page }) => {
  test.setTimeout(120_000)
  const row = () => page.getByRole('region', { name: 'Occurrences' }).getByRole('listitem').filter({ has: page.getByText('Gym', { exact: true }) })
  const carried = () => page.getByTestId('carried-in').getByRole('listitem').filter({ has: page.getByText('Gym', { exact: true }) })
  const totals = () => page.getByRole('heading', { level: 3, name: 'EUR' }).locator('..')
  const value = (label: string) => totals().getByText(label, { exact: true }).locator('xpath=following-sibling::dd[1]')
  const monthView = async (month: string) => {
    await page.goto('/month?month=' + month)
    await expect(page.getByRole('heading', { level: 1, name: new RegExp(month === d.month ? d.monthName : d.laterMonthName) })).toBeVisible()
  }
  const plan = async (form: ReturnType<typeof page.getByRole>, date: string) => {
    const field = form.getByLabel(/Planned date/)
    await field.fill(date)
  }

  await test.step('sign in, connect the simulated bank and import its payment', async () => {
    await page.goto('/')
    await page.getByLabel('Username').fill(JOURNEY_USER)
    await page.getByLabel('Password').fill(JOURNEY_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page).toHaveURL(/\/month/)
    await page.goto('/connections')
    await page.getByRole('button', { name: 'Connect a bank' }).first().click()
    const dialog = page.getByRole('dialog', { name: 'Connect a bank' })
    await dialog.getByLabel('Country of the bank').fill('FI')
    await page.keyboard.press('Enter')
    await dialog.getByLabel('Bank', { exact: true }).selectOption('Example Bank')
    await dialog.getByRole('button', { name: 'Continue to your bank' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Example Bank is connected' })).toBeVisible()
    await page.getByRole('button', { name: 'Fetch accounts now' }).click()
    await expect(page.getByText('Fetched 1 account(s).')).toBeVisible()
  })

  await test.step('record the commitment', async () => {
    const res = await post(page, '/api/v1/commitments', {
      name: 'Gym', kind: 'service', amount: { amount: '50.00', currency: 'EUR' },
      schedule: { version: 1, frequency: 'monthly', interval: 1, day: 20 }, start_date: d.month + '-01', end_date: null,
    })
    expect(res.status(), await res.text()).toBe(201)
    await monthView(d.month)
    await expect(row()).toContainText('Upcoming')
    await expect(row()).toContainText('Original due date ' + d.due)
    await expect(value('Planned this month')).toContainText('50.00 EUR')
  })

  await test.step('defer within the month: the plan is saved only when the server confirms, nothing is paid', async () => {
    await row().getByRole('button', { name: /Plan a later date for Gym/ }).click()
    const form = page.getByRole('form', { name: 'Plan a later payment date' })
    await expect(form).toContainText('does not mark anything paid')
    // A date that is not after the original due date is refused before anything is sent.
    await plan(form, d.due)
    await form.getByRole('button', { name: 'Save plan' }).click()
    await expect(form).toContainText('The planned date must be after the original due date, ' + d.due + '.')
    await plan(form, d.within)
    await expect(form.getByTestId('plan-effect')).toContainText('It stays planned in ' + d.monthName)
    await form.getByLabel(/Reason/).fill('paid on the 27th')
    await form.getByRole('button', { name: 'Save plan' }).click()
    const notice = page.getByTestId('plan-notice')
    await expect(notice).toContainText('Plan saved')
    await expect(notice).toContainText('is planned for ' + d.within)
    await expect(notice).toContainText('Nothing is marked paid')
    // Original due date, status and planned catch-up date are three separate labelled facts.
    const summary = row().getByTestId('plan-summary')
    await expect(summary).toContainText('Original due date' + d.due)
    await expect(summary).toContainText('Planned catch-up date' + d.within)
    await expect(row()).toContainText('Upcoming')
    await expect(row()).toContainText('Planned for ' + d.within)
    await expect(value('Planned this month')).toContainText('50.00 EUR')
    await expect(page.getByTestId('carried-in')).toHaveCount(0)
  })

  await test.step('carry it to the next month: still owed here, planned there, listed apart', async () => {
    await row().getByRole('button', { name: /Manage the plan for Gym/ }).click()
    await page.getByRole('button', { name: 'Change plan' }).click()
    const form = page.getByRole('form', { name: 'Change the plan' })
    await plan(form, d.carried)
    await expect(form.getByTestId('plan-effect')).toContainText('In ' + d.laterMonthName + ' it is planned')
    await form.getByRole('button', { name: 'Save changes' }).click()
    await expect(page.getByTestId('plan-notice')).toContainText('Plan changed')
    await expect(value('Outstanding')).toContainText('50.00 EUR')
    await expect(value('Planned this month')).toContainText('0.00 EUR')
    await expect(page.getByTestId('plan-figures-EUR')).toContainText('Deferred to a later month')
    await expect(row()).toContainText('Upcoming')
    await monthView(d.laterMonth)
    // Two outstanding 50.00 occurrences: planned 100.00 here, nothing counted twice.
    await expect(carried()).toContainText('Original due date ' + d.due + ', in ' + d.monthName)
    await expect(carried()).toContainText('Planned for ' + d.carried + ' in ' + d.laterMonthName)
    await expect(value('Scheduled this month')).toContainText('50.00 EUR')
    await expect(value('Planned this month')).toContainText('100.00 EUR')
    await expect(page.getByTestId('plan-figures-EUR')).toContainText('Carried in from earlier months')
    await expect(row()).toHaveCount(1) // the month's own occurrence, never merged with the carried one
  })

  await test.step('the outlook shows what is still due and what is planned, with the deferred amount apart', async () => {
    await page.goto('/outlook?through=' + d.laterMonth)
    const card = page.getByTestId('outlook-EUR')
    await expect(card.getByText('Recorded commitments still due', { exact: true }).locator('xpath=following-sibling::dd[1]')).toContainText('100.00 EUR')
    await expect(card.getByText('Planned to be paid through ' + d.laterMonthName).locator('xpath=following-sibling::dd[1]')).toContainText('100.00 EUR')
    await expect(card.getByTestId('outlook-planned-deferred-in')).toContainText('of which deferred in')
    await expect(card.getByTestId('outlook-planned-deferred-in').locator('xpath=following-sibling::dd[1]')).toContainText('50.00 EUR')
    await page.goto('/outlook?through=' + d.month)
    await expect(page.getByTestId('outlook-planned-beyond')).toContainText('Still due but planned after ' + d.monthName)
  })

  await test.step('undo the plan from the carried-in row: the occurrence is planned in its own month again', async () => {
    await monthView(d.laterMonth)
    await carried().getByRole('button', { name: /Manage the plan for Gym/ }).click()
    await page.getByRole('button', { name: 'Undo plan' }).click()
    const confirm = page.getByRole('group', { name: 'Undo this plan' })
    await expect(confirm).toContainText('Nothing is marked paid and the history of the plan is kept')
    await confirm.getByLabel('Reason (optional)').fill('changed my mind')
    await confirm.getByRole('button', { name: 'Undo plan' }).click()
    await expect(page.getByTestId('plan-notice')).toContainText('Plan undone')
    await expect(page.getByTestId('carried-in')).toHaveCount(0)
    await expect(value('Planned this month')).toContainText('50.00 EUR')
    await monthView(d.month)
    await expect(row().getByTestId('plan-summary')).toHaveCount(0)
    await expect(value('Planned this month')).toContainText('50.00 EUR')
  })

  await test.step('plan it again to the later month, import again and let matching pay it from the planned date', async () => {
    await row().getByRole('button', { name: /Plan a later date for Gym/ }).click()
    const form = page.getByRole('form', { name: 'Plan a later payment date' })
    await plan(form, d.carried)
    await form.getByRole('button', { name: 'Save plan' }).click()
    await expect(page.getByTestId('plan-notice')).toContainText('Plan saved')
    // Import again: the plan reads and writes no bank facts, and the import changes nothing about it.
    await page.goto('/connections')
    const refreshed = page.waitForResponse((r) => /\/bank-connections\/[^/]+\/refresh$/.test(r.url()) && r.request().method() === 'POST')
    await page.getByRole('article', { name: 'Example Bank' }).getByRole('button', { name: 'Refresh now' }).click()
    expect((await refreshed).ok()).toBe(true)
    // The refresh imports the payment again (idempotently) and runs automatic matching. The payment is dated on the planned
    // date, far from the due date: the plan's date is what lets the engine confirm it.
    await monthView(d.month)
    await expect(row()).toContainText('Paid')
    await expect(row()).toContainText('Bank match')
    await expect(value('Outstanding')).toContainText('0.00 EUR')
    // The plan stays as history on the paid occurrence; it was never what paid it.
    await expect(row()).toContainText('Planned for ' + d.carried)
    await expect(row().getByRole('button', { name: /Manage the plan for Gym/ })).toBeVisible()
    await page.goto('/matches')
    await page.getByRole('button', { name: 'Confirmed', exact: true }).click()
    await expect(page.getByRole('article').filter({ hasText: 'Gym' }).getByTestId('decision')).toContainText('Confirmed automatically by the matching engine')
  })

  await test.step('at 390 px the plan, its fees and the carried-in list reflow without sideways scrolling', async () => {
    await page.setViewportSize({ width: 390, height: 844 })
    await monthView(d.month)
    await row().getByRole('button', { name: /Manage the plan for Gym/ }).click()
    await page.getByRole('button', { name: 'Record a fee or interest' }).click()
    await expect(page.getByRole('group', { name: 'Record a fee or interest' })).toBeVisible()
    const fit = async () => {
      const { scroll, client } = await page.evaluate(() => ({ scroll: document.documentElement.scrollWidth, client: document.documentElement.clientWidth }))
      expect(scroll, 'no horizontal scrolling').toBeLessThanOrEqual(client)
    }
    await fit()
    if (process.env.DEFERRAL_SHOTS_DIR) await page.screenshot({ path: process.env.DEFERRAL_SHOTS_DIR + '/plan-390.png', fullPage: true })
    await monthView(d.laterMonth)
    await expect(carried()).toBeVisible()
    await fit()
    if (process.env.DEFERRAL_SHOTS_DIR) await page.screenshot({ path: process.env.DEFERRAL_SHOTS_DIR + '/carried-390.png', fullPage: true })
    await page.setViewportSize({ width: 1280, height: 900 })
  })

  await test.step('a reload shows the same plan and totals', async () => {
    await monthView(d.month)
    await page.reload()
    await expect(row().getByTestId('plan-summary')).toContainText('Planned catch-up date' + d.carried)
    await expect(row()).toContainText('Paid')
  })
})
