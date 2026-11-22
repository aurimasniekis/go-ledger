import { expect, test } from './fixtures'
import { COMMITMENTS_LEDGER_PORT, JOURNEY_PASSWORD, JOURNEY_USER, deferralDates } from './journey-data.mjs'

// The commitment forms (CM-UI): the real ledger binary with its embedded UI and a throwaway database (journey-server.mjs,
// commitments scenario). A commitment is created in the UI, its occurrence is read on the monthly view, the commitment is
// edited (the occurrence follows), ended with an Undo and deleted. Nothing is stubbed and nothing reaches a live bank.
// Next month is used so the occurrence is upcoming on any day of the year (days stay at or below 28).
test.use({ baseURL: `http://127.0.0.1:${COMMITMENTS_LEDGER_PORT}`, timezoneId: 'UTC' })

const d = deferralDates()

test('create a commitment in the UI, see its occurrence, edit, end with undo and delete it', async ({ page }) => {
  test.setTimeout(120_000)
  const monthRow = () => page.getByRole('region', { name: 'Occurrences' }).getByRole('listitem').filter({ has: page.getByText('Web hosting', { exact: true }) })

  await test.step('sign in; the empty month view offers New commitment', async () => {
    await page.goto('/')
    await page.getByLabel('Username').fill(JOURNEY_USER)
    await page.getByLabel('Password').fill(JOURNEY_PASSWORD)
    await page.getByRole('button', { name: 'Sign in' }).click()
    await expect(page).toHaveURL(/\/month/)
    await expect(page.getByRole('heading', { name: /No commitments fall due/ })).toBeVisible()
    await page.getByRole('link', { name: 'New commitment' }).click()
    await expect(page).toHaveURL(/\/commitments\/new$/)
  })

  await test.step('a form with a missing name and amount is refused before anything is sent', async () => {
    await page.getByRole('button', { name: 'Create commitment' }).click()
    await expect(page.getByText('Give the commitment a name.')).toBeVisible()
    await expect(page.getByText(/Enter an amount greater than zero/)).toBeVisible()
  })

  await test.step('create the commitment', async () => {
    await page.getByLabel('Name').fill('Web hosting')
    await page.getByLabel('Kind').selectOption('server')
    await page.getByLabel('Priority').selectOption('essential')
    await page.getByLabel('Amount per due date').fill('19.900')
    await page.getByLabel('Currency').fill('EUR')
    await page.getByLabel('Day of the month').fill('20')
    await page.getByLabel('Start date').fill(d.month + '-01')
    // A date that does not exist (day 32) is refused by the form's own check, next to its field.
    await page.getByLabel('Start date').fill(d.month + '-32')
    await page.getByRole('button', { name: 'Create commitment' }).click()
    await expect(page.getByText(/Enter the start date as YYYY-MM-DD/)).toBeVisible()
    await page.getByLabel('Start date').fill(d.month + '-01')
    await page.getByRole('button', { name: 'Create commitment' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Web hosting' })).toBeVisible()
    await expect(page.getByRole('status').filter({ hasText: 'Commitment created.' })).toBeVisible()
    await expect(page.getByTestId('commitment-summary')).toContainText('Monthly on the 20th')
    // The exact text typed is what was kept.
    await expect(page.getByLabel('Amount per due date')).toHaveValue('19.900')
  })

  await test.step('the occurrence is on the monthly view with its amount', async () => {
    await page.getByRole('link', { name: 'monthly view', exact: true }).click()
    await expect(page.getByRole('heading', { level: 1, name: new RegExp(d.monthName) })).toBeVisible()
    await expect(monthRow()).toBeVisible()
    await expect(monthRow()).toContainText(/19\.900\s+EUR/)
    await expect(monthRow()).toContainText(d.month + '-20')
  })

  await test.step('the list shows amount and schedule; an edit moves the future occurrence', async () => {
    await page.goto('/commitments')
    const row = page.getByRole('list', { name: 'Commitments' }).getByRole('listitem').filter({ hasText: 'Web hosting' })
    await expect(row).toContainText('Monthly on the 20th')
    await expect(row).toContainText(/19\.900\s+EUR/)
    await row.getByRole('link', { name: 'Edit Web hosting' }).click()
    await page.getByLabel('Day of the month').fill('25')
    await expect(page.getByTestId('occurrence-note')).toContainText('Occurrences already marked paid never change')
    await page.getByRole('button', { name: 'Save changes' }).click()
    await expect(page.getByRole('status').filter({ hasText: 'Changes saved.' })).toBeVisible()
    await page.goto('/month?month=' + d.month)
    await expect(monthRow()).toContainText(d.month + '-25')
  })

  await test.step('end the commitment, then undo', async () => {
    await page.goto('/commitments')
    await page.getByRole('link', { name: 'Edit Web hosting' }).click()
    await page.getByRole('button', { name: 'End commitment' }).click()
    const dialog = page.getByRole('dialog', { name: 'End Web hosting' })
    await dialog.getByLabel('End date').fill(d.month + '-10')
    await dialog.getByRole('button', { name: 'End commitment' }).click()
    await expect(page.getByRole('status').filter({ hasText: /now ends on/ })).toBeVisible()
    // The occurrence on the 25th falls after the new end date, so the server removed it.
    const occurrences = async () => ((await (await page.request.get('/api/v1/occurrences?month=' + d.month)).json()) as { items: unknown[] }).items.length
    expect(await occurrences()).toBe(0)
    await page.getByRole('button', { name: /Undo/ }).click()
    await expect(page.getByRole('status').filter({ hasText: 'End date restored.' })).toBeVisible()
    expect(await occurrences()).toBe(1)
    await page.goto('/month?month=' + d.month)
    await expect(monthRow()).toBeVisible()
  })

  await test.step('delete it after a confirmation', async () => {
    await page.goto('/commitments')
    await page.getByRole('link', { name: 'Edit Web hosting' }).click()
    await page.getByRole('button', { name: 'Delete commitment' }).click()
    await page.getByRole('alertdialog').getByRole('button', { name: 'Delete commitment' }).click()
    await expect(page.getByRole('heading', { level: 1, name: 'Commitments' })).toBeVisible()
    await expect(page.getByText('No commitments yet')).toBeVisible()
  })
})

test('the form fits a 390 px screen without sideways scrolling', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 })
  await page.goto('/')
  await page.getByLabel('Username').fill(JOURNEY_USER)
  await page.getByLabel('Password').fill(JOURNEY_PASSWORD)
  await page.getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/month/)
  await page.goto('/commitments/new')
  await expect(page.getByRole('heading', { level: 1, name: 'New commitment' })).toBeVisible()
  await page.getByLabel('Grace').selectOption('period')
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)
  expect(overflow).toBeLessThanOrEqual(0)
})
