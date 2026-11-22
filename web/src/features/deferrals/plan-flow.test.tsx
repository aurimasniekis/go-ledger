import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { installApi, renderApp, signedIn, type Reply, type Req } from '@/test/fake-api'
import { item, payment, summary } from '@/features/month/fixtures'
import type { MonthItem, MonthSummary } from '@/features/month/types'
import { fee, plan } from './fixtures'

const MONTH = '/api/v1/occurrences/summary'
const PLANS = '/api/v1/deferral-plans'
const eur = (amount: string) => ({ amount, currency: 'EUR' })
const json = (status: number, body: unknown): Reply => ({ status, json: body })

let calls: Req[]
let month: MonthSummary
let routes: Record<string, (r: Req) => Reply | Promise<Reply>>

beforeEach(() => {
  month = summary([item('a', { due_date: '2026-11-05', payment: payment({ status: 'overdue', timing: 'overdue', outstanding_amount: eur('10.00') }) })])
  routes = {}
  calls = installApi({
    ...signedIn(),
    ['GET ' + MONTH]: () => json(200, month),
    ['POST ' + PLANS]: (r) => routes['POST ' + PLANS]!(r),
    ['POST ' + PLANS + '/p1/undo']: (r) => routes['undo']!(r),
    ['POST ' + PLANS + '/p1/adjustments']: (r) => routes['addFee']!(r),
    ['POST ' + PLANS + '/p1/adjustments/f1/paid']: (r) => routes['paid']!(r),
    ['GET ' + PLANS + '/p1']: () => json(200, plan({ version: 3 })),
    'GET /api/v1/payment-matches': () => json(200, { items: [], next_cursor: null }),
  })
})
afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const open = () => renderApp('/month?month=2026-11')
const withPlan = (p = plan(), over: Partial<MonthItem> = {}) => {
  month = summary([item('a', { due_date: '2026-11-05', payment: payment({ status: 'overdue', timing: 'overdue', outstanding_amount: eur('10.00') }), deferral: p, ...over })])
}

describe('Planning a later payment date', () => {
  it('shows the plan as saved only after the server answers, with the effect on both months and an undo', async () => {
    let release: (r: Reply) => void = () => {}
    routes['POST ' + PLANS] = () => new Promise<Reply>((res) => (release = res))
    open()
    await userEvent.click(await screen.findByRole('button', { name: /Plan a later date for Rent a/ }))
    const form = screen.getByRole('form', { name: 'Plan a later payment date' })
    expect(form).toHaveTextContent('does not mark anything paid')
    await userEvent.type(within(form).getByLabelText(/Planned date/), '2026-12-05')
    expect(within(form).getByTestId('plan-effect')).toHaveTextContent('In November 2026 it stays owed and keeps its original due date and overdue status')
    expect(within(form).getByTestId('plan-effect')).toHaveTextContent('In December 2026 it is planned')
    await userEvent.type(within(form).getByLabelText(/Reason/), 'salary arrives late')
    await userEvent.click(within(form).getByRole('button', { name: 'Save plan' }))
    // Pending: not saved, inputs locked, nothing claims it is planned.
    expect(await within(form).findByText('Not saved yet')).toBeInTheDocument()
    expect(screen.queryByTestId('plan-notice')).toBeNull()
    expect(within(form).getByLabelText(/Planned date/)).toBeDisabled()
    const post = calls.find((c) => c.method === 'POST' && c.path === PLANS)!
    expect(post.body).toEqual({ occurrence_id: 'a', plan_type: 'personal', planned_date: '2026-12-05', reason: 'salary arrives late' })
    withPlan(plan({ planned_date: '2026-12-05', reason: 'salary arrives late' }))
    release(json(201, plan({ planned_date: '2026-12-05', reason: 'salary arrives late' })))
    const notice = await screen.findByTestId('plan-notice')
    expect(notice).toHaveTextContent('Plan saved')
    expect(notice).toHaveTextContent('planned for 2026-12-05')
    expect(notice).toHaveTextContent('Nothing is marked paid')
    // The row keeps its overdue status and shows the plan beside it.
    const row = screen.getByText('Rent a').closest('li')!
    expect(within(row).getByText('Overdue')).toBeInTheDocument()
    expect(within(row).getByText('Planned for 2026-12-05')).toBeInTheDocument()
    expect(within(row).getByTestId('plan-summary')).toHaveTextContent('Original due date2026-11-05; overdue since this date')
    expect(within(row).getByTestId('plan-summary')).toHaveTextContent('Planned catch-up date2026-12-05')
  })

  it('refuses an impossible date before sending, and keeps what was typed when the server refuses', async () => {
    routes['POST ' + PLANS] = () => json(409, { code: 'nothing_outstanding', message: 'nothing is outstanding', request_id: 'r' })
    open()
    await userEvent.click(await screen.findByRole('button', { name: /Plan a later date for/ }))
    const form = screen.getByRole('form', { name: 'Plan a later payment date' })
    const date = within(form).getByLabelText(/Planned date/)
    await userEvent.type(date, '2026-11-05')
    await userEvent.click(within(form).getByRole('button', { name: 'Save plan' }))
    expect(await within(form).findByText('The planned date must be after the original due date, 2026-11-05.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST' && c.path === PLANS)).toBe(false)
    await userEvent.clear(date)
    await userEvent.type(date, '2026-12-05')
    await userEvent.click(within(form).getByRole('button', { name: 'Save plan' }))
    const alert = await within(form).findByRole('alert')
    expect(alert).toHaveTextContent('Plan not saved')
    expect(alert).toHaveTextContent('Nothing is owed on this occurrence any more')
    expect(alert).toHaveTextContent('Nothing was changed')
    expect(date).toHaveValue('2026-12-05')
  })

  it('offers no plan button for a paid occurrence', async () => {
    month = summary([item('a', { payment: payment({ status: 'paid', timing: null, outstanding_amount: eur('0.00') }) })])
    open()
    await screen.findByText('Rent a')
    expect(screen.queryByRole('button', { name: /Plan a later date/ })).toBeNull()
  })
})

describe('A plan on the month view', () => {
  it('keeps original due date, overdue, planned date and documented grace as separate labelled facts', async () => {
    withPlan(plan({ plan_type: 'documented_grace', planned_date: '2026-11-28', planned_month: '2026-11', grace_until: '2026-11-28', within_grace: true }))
    open()
    const summaryBox = await screen.findByTestId('plan-summary')
    expect(summaryBox).toHaveTextContent('Original due date2026-11-05; overdue since this date')
    expect(summaryBox).toHaveTextContent('Planned catch-up date2026-11-28 (November 2026); documented grace period')
    expect(summaryBox).toHaveTextContent('Documented graceWithin documented grace until 2026-11-28')
    expect(summaryBox).toHaveTextContent('A plan is not a payment')
    // An unknown grace period stays its own sentence, separate from the plan.
    const row = summaryBox.closest('li')!
    expect(within(row).queryByTestId('leeway-unknown')).toBeNull()
  })

  it('says why a plan is missed, for the amount and for a fee, in words', async () => {
    withPlan(plan({ missed: true, missed_reason: 'principal' }))
    const { unmount } = open()
    expect(await screen.findByText('Plan date passed, still owed')).toBeInTheDocument()
    unmount()
    withPlan(plan({ missed: true, missed_reason: 'fee', occurrence_status: 'paid' }), { payment: payment({ status: 'paid', timing: null, outstanding_amount: eur('0.00') }) })
    open()
    expect(await screen.findByText('Plan date passed, fee still planned')).toBeInTheDocument()
  })

  it('lists carried-in occurrences apart with their own month and due date, and the planned figures beside the obligation totals', async () => {
    month = summary([item('b', { commitment_name: 'Gym', due_date: '2026-11-15' })], {
      carried_in: [item('a', { month: '2026-10', due_date: '2026-10-05', deferral: plan({ planned_date: '2026-11-20', planned_month: '2026-11', original_due_date: '2026-10-05' }), payment: payment({ status: 'overdue', timing: 'overdue' }) })],
      totals: [{
        currency: 'EUR', due: eur('10.00'), paid: eur('0.00'), outstanding: eur('10.00'), overdue: eur('0.00'), overpaid: eur('0.00'), occurrence_count: 1, overdue_count: 0, unconverted_evidence_count: 0,
        planned: eur('25.00'), deferred_out: eur('0.00'), carried_in: eur('10.00'), adjustments: eur('5.00'),
      }],
    })
    open()
    const carried = await screen.findByTestId('carried-in')
    expect(within(carried).getByRole('heading', { name: 'Carried in from earlier months' })).toBeInTheDocument()
    expect(carried).toHaveTextContent('Original due date 2026-10-05, in October 2026')
    expect(carried).toHaveTextContent('Planned for 2026-11-20 in November 2026')
    expect(within(carried).getByRole('link', { name: 'October 2026' })).toHaveAttribute('href', '/month?month=2026-10')
    // The month's own list holds its own occurrence only; the carried-in one is never merged into it.
    const own = screen.getByRole('region', { name: 'Occurrences' })
    expect(within(own).getByText('Gym')).toBeInTheDocument()
    expect(within(own).queryByText('Rent a')).toBeNull()
    const figures = screen.getByTestId('plan-figures-EUR')
    expect(figures).toHaveTextContent('Carried in from earlier months')
    expect(figures).toHaveTextContent('Fees and charges still planned')
    expect(figures).not.toHaveTextContent('Deferred to a later month')
    const totals = screen.getByRole('heading', { level: 3, name: 'EUR' }).closest('li')!
    expect(within(totals).getByText('Outstanding').nextElementSibling).toHaveTextContent('10.00 EUR')
    expect(within(totals).getByTestId('planned-EUR')).toHaveTextContent('25.00 EUR')
  })
})

describe('Changing and undoing a plan', () => {
  const manage = async () => {
    open()
    await userEvent.click(await screen.findByRole('button', { name: /Manage the plan for Rent a/ }))
    return screen.getByRole('heading', { name: 'Plan for Rent a' }).closest('section')!
  }

  it('says why undo is unavailable while a fee is planned, then lets a marked-paid fee through', async () => {
    withPlan(plan({ version: 3, adjustments: [fee()], adjustment_total: eur('5.00'), unpaid_adjustment_total: eur('5.00') }))
    routes['paid'] = (r) => {
      expect(r.headers['If-Match']).toBe('"3"')
      withPlan(plan({ version: 4, adjustments: [fee({ paid_at: '2026-11-12T09:00:00Z' })], adjustment_total: eur('5.00') }))
      return json(200, plan({ version: 4 }))
    }
    routes['undo'] = (r) => {
      expect(r.headers['If-Match']).toBe('"4"')
      expect(r.body).toEqual({})
      month = summary([item('a', { due_date: '2026-11-05' })])
      return json(200, plan({ state: 'undone', version: 5 }))
    }
    const panel = await manage()
    const undo = within(panel).getByRole('button', { name: 'Undo plan' })
    expect(undo).toBeDisabled()
    expect(undo).toHaveAccessibleDescription(/Undo is not available while a fee on this plan is still planned/)
    expect(within(panel).getByTestId('fee-planned')).toHaveTextContent('Planned, not paid')
    await userEvent.click(within(panel).getByRole('button', { name: 'Mark paid' }))
    expect(await within(panel).findByTestId('fee-paid')).toHaveTextContent('Marked paid 2026-11-12 09:00 UTC')
    expect(within(panel).getByText(/matched to no bank transaction/)).toBeInTheDocument()
    await waitFor(() => expect(within(panel).getByRole('button', { name: 'Undo plan' })).toBeEnabled())
    await userEvent.click(within(panel).getByRole('button', { name: 'Undo plan' }))
    await userEvent.click(within(panel).getByRole('group', { name: 'Undo this plan' }).querySelector('button')!)
    const notice = await screen.findByTestId('plan-notice')
    expect(notice).toHaveTextContent('Plan undone')
    expect(notice).toHaveTextContent('Nothing was marked paid')
    expect(screen.queryByTestId('plan-summary')).toBeNull()
  })

  it('keeps the fee form open with what was typed, and focuses it, when the server refuses; closes it only on success', async () => {
    withPlan(plan({ version: 3 }))
    routes['addFee'] = () => json(409, { code: 'version_conflict', message: 'stale', request_id: 'r', reconciliation: { current_version: 4 } })
    const panel = await manage()
    await userEvent.click(within(panel).getByRole('button', { name: 'Record a fee or interest' }))
    const form = within(panel).getByRole('group', { name: 'Record a fee or interest' })
    await userEvent.type(within(form).getByLabelText(/Amount/), '5.00')
    await userEvent.type(within(form).getByLabelText(/Note/), 'late charge')
    await userEvent.click(within(form).getByRole('button', { name: 'Save fee' }))
    expect(await within(panel).findByText('Not saved')).toBeInTheDocument()
    const again = within(panel).getByRole('group', { name: 'Record a fee or interest' })
    expect(within(again).getByLabelText(/Amount/)).toHaveValue('5.00')
    expect(within(again).getByLabelText(/Note/)).toHaveValue('late charge')
    await waitFor(() => expect(within(again).getByLabelText(/Amount/)).toHaveFocus())
    routes['addFee'] = () => json(201, plan({ version: 4 }))
    await userEvent.click(within(again).getByRole('button', { name: 'Save fee' }))
    await waitFor(() => expect(within(panel).queryByRole('group', { name: 'Record a fee or interest' })).toBeNull())
  })

  it('refuses a zero amount written with extra zeros before sending', async () => {
    withPlan(plan({ version: 3 }))
    routes['addFee'] = () => json(201, plan({ version: 4 }))
    const panel = await manage()
    await userEvent.click(within(panel).getByRole('button', { name: 'Record a fee or interest' }))
    const form = within(panel).getByRole('group', { name: 'Record a fee or interest' })
    await userEvent.type(within(form).getByLabelText(/Amount/), '00.00')
    await userEvent.click(within(form).getByRole('button', { name: 'Save fee' }))
    expect(await within(form).findByText('Enter a positive amount such as 5.00.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST' && c.path.endsWith('/adjustments'))).toBe(false)
  })

  it('shows the server refusal when undo is refused for a fee, and keeps the plan', async () => {
    withPlan(plan({ version: 3 }))
    routes['undo'] = () => json(409, { code: 'deferral_adjustments_unpaid', message: 'a fee is planned', request_id: 'r' })
    const panel = await manage()
    await userEvent.click(within(panel).getByRole('button', { name: 'Undo plan' }))
    await userEvent.click(within(panel).getByRole('group', { name: 'Undo this plan' }).querySelector('button')!)
    const alert = await within(panel).findByText(/A fee on this plan is still planned\. Mark it paid or remove it first/)
    expect(alert.closest('[role=alert]')).toHaveTextContent('Undo failed')
    expect(alert.closest('[role=alert]')).toHaveTextContent('The plan is still in place')
    expect(screen.getByTestId('plan-summary')).toBeInTheDocument()
  })

  it('uses the plan version on screen for a change and reports a version conflict in words', async () => {
    withPlan(plan({ version: 2 }))
    routes['PATCH'] = () => json(409, { code: 'version_conflict', message: 'stale', request_id: 'r', reconciliation: { current_version: 3 } })
    calls = installApi({
      ...signedIn(),
      ['GET ' + MONTH]: () => json(200, month),
      ['PATCH ' + PLANS + '/p1']: (r) => routes['PATCH']!(r),
      'GET /api/v1/payment-matches': () => json(200, { items: [], next_cursor: null }),
    })
    const panel = await manage()
    await userEvent.click(within(panel).getByRole('button', { name: 'Change plan' }))
    const form = screen.getByRole('form', { name: 'Change the plan' })
    const date = within(form).getByLabelText(/Planned date/)
    await userEvent.clear(date)
    await userEvent.type(date, '2026-12-20')
    await userEvent.click(within(form).getByRole('button', { name: 'Save changes' }))
    const patch = await waitFor(() => calls.find((c) => c.method === 'PATCH')!)
    expect(patch.headers['If-Match']).toBe('"2"')
    expect(patch.body).toEqual({ plan_type: 'personal', planned_date: '2026-12-20', reason: null })
    expect(await within(form).findByRole('alert')).toHaveTextContent('This plan changed elsewhere')
  })
})
