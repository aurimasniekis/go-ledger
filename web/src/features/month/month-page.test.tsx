import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '@/App'
import { setCsrfToken } from '@/lib/csrf'
import { item, payment, summary } from './fixtures'

type Call = { method: string; url: string; headers: Record<string, string>; body?: string }
let calls: Call[]
let responder: (c: Call) => Response | Promise<Response>

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

beforeEach(() => {
  calls = []
  setCsrfToken('csrf-test')
  vi.stubGlobal('fetch', async (url: string, init?: RequestInit) => {
    const c: Call = { method: init?.method ?? 'GET', url, headers: (init?.headers ?? {}) as Record<string, string>, body: init?.body as string | undefined }
    calls.push(c)
    return responder(c)
  })
})
afterEach(() => vi.unstubAllGlobals())

const open = () => render(<App history={createMemoryHistory({ initialEntries: ['/month?month=2026-11'] })} />)

describe('Month page', () => {
  it('loads the month from the summary endpoint and shows rows', async () => {
    responder = () => json(200, summary([item('a')]))
    open()
    expect(await screen.findByText('Rent a')).toBeInTheDocument()
    expect(calls[0]!.url).toBe('/api/v1/occurrences/summary?month=2026-11')
    expect(screen.getByRole('heading', { level: 2, name: 'November 2026' })).toBeInTheDocument()
  })

  it('shows the failed state when the server errors, and recovers on retry', async () => {
    let n = 0
    responder = () => (n++ < 1 ? json(400, { code: 'validation_failed', message: 'month is out of range', request_id: 'r1' }) : json(200, summary([item('a')])))
    open()
    expect(await screen.findByRole('alert')).toHaveTextContent('month is out of range')
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText('Rent a')).toBeInTheDocument()
  })

  it('records a payment: Not saved yet while pending, saved only after the server answers, with undo', async () => {
    let release: (r: Response) => void = () => {}
    const posted = new Promise<Response>((res) => (release = res))
    let saved = false
    responder = (c) => {
      if (c.method === 'POST') return posted
      if (c.method === 'DELETE') { saved = false; return new Response(null, { status: 204 }) }
      return json(200, saved ? summary([item('a', { payment: payment({ status: 'paid' }) })]) : summary([item('a')]))
    }
    open()
    await userEvent.click(await screen.findByRole('button', { name: 'Record a payment' }))
    const form = await screen.findByRole('form', { name: 'Record a payment' })
    expect(within(form).getByLabelText(/Amount paid/)).toHaveValue('10.00') // the server's outstanding amount, not computed here
    expect(within(form).getByLabelText('Paid on')).toHaveValue('2026-11-21')
    await userEvent.click(within(form).getByRole('button', { name: 'Save payment' }))

    expect(await within(form).findByText('Not saved yet')).toBeInTheDocument()
    expect(screen.queryByText(/^Saved:/)).toBeNull()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.url).toBe('/api/v1/occurrences/a/manual-payments')
    expect(post.headers['X-CSRF-Token']).toBe('csrf-test')
    expect(JSON.parse(post.body!)).toEqual({ amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-21' })

    saved = true
    release(json(201, { id: 'p1', occurrence_id: 'a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-21', version: 3 }))
    expect(await screen.findByText(/^Saved: 10.00 EUR paid on 2026-11-21 for Rent a/)).toBeInTheDocument()
    await waitFor(() => expect(screen.getAllByText('Paid').length).toBeGreaterThan(0))

    await userEvent.click(screen.getByRole('button', { name: 'Undo this payment' }))
    expect(await screen.findByText(/Payment removed/)).toBeInTheDocument()
    const del = calls.find((c) => c.method === 'DELETE')!
    expect(del.url).toBe('/api/v1/occurrences/a/manual-payments/p1')
    expect(del.headers['If-Match']).toBe('"3"')
    await waitFor(() => expect(screen.queryByText('Paid', { selector: '[data-status] *' })).toBeNull())
  })

  it('a failed save says it was not saved, keeps the typed values and changes no status', async () => {
    responder = (c) => (c.method === 'POST' ? json(422, { code: 'validation_failed', message: 'amount must be a decimal', field: 'amount', request_id: 'r' }) : json(200, summary([item('a')])))
    open()
    await userEvent.click(await screen.findByRole('button', { name: 'Record a payment' }))
    const form = await screen.findByRole('form', { name: 'Record a payment' })
    const amount = within(form).getByLabelText(/Amount paid/)
    await userEvent.clear(amount)
    await userEvent.type(amount, 'abc')
    await userEvent.click(within(form).getByRole('button', { name: 'Save payment' }))
    const alert = await within(form).findByRole('alert')
    expect(alert).toHaveTextContent('Payment not saved')
    expect(alert).toHaveTextContent('Nothing was recorded')
    expect(amount).toHaveValue('abc')
    expect(amount).toHaveAttribute('aria-invalid', 'true')
    expect(screen.queryByText(/^Saved:/)).toBeNull()
    expect(screen.queryByText('Paid', { selector: '[data-status] *' })).toBeNull()
  })

  it('a failed undo says the payment is still recorded', async () => {
    responder = (c) => {
      if (c.method === 'POST') return json(201, { id: 'p1', occurrence_id: 'a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-21', version: 1 })
      if (c.method === 'DELETE') return json(409, { code: 'version_conflict', message: 'the payment changed', request_id: 'r' })
      return json(200, summary([item('a')]))
    }
    open()
    await userEvent.click(await screen.findByRole('button', { name: 'Record a payment' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Save payment' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Undo this payment' }))
    expect(await screen.findByText(/The payment is still recorded/)).toBeInTheDocument()
  })

  it('the primary action is disabled when every occurrence is paid', async () => {
    responder = () => json(200, summary([item('a', { payment: payment({ status: 'paid' }) })]))
    open()
    expect(await screen.findByRole('button', { name: 'Record a payment' })).toBeDisabled()
  })

  it('moves focus to the undo notice after a save, then back to the opener after Dismiss', async () => {
    responder = (c) =>
      c.method === 'POST' ? json(201, { id: 'p1', occurrence_id: 'a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-21', version: 1 })
      : c.method === 'DELETE' ? new Response(null, { status: 204 }) : json(200, summary([item('a')]))
    open()
    const opener = await screen.findByRole('button', { name: /^Record payment for Rent a/ })
    await userEvent.click(opener)
    await userEvent.click(await screen.findByRole('button', { name: 'Save payment' }))
    const notice = await screen.findByText(/^Saved:/)
    await waitFor(() => expect(notice.closest('[role="status"]')).toHaveFocus())
    await userEvent.click(screen.getByRole('button', { name: 'Undo this payment' }))
    await screen.findByText(/Payment removed/)
    await waitFor(() => expect(screen.getByText(/Payment removed/).closest('[role="status"]')).toHaveFocus())
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.getByRole('button', { name: /^Record payment for Rent a/ })).toHaveFocus())
  })

  it('returns focus to the opener when the form is cancelled', async () => {
    responder = () => json(200, summary([item('a')]))
    open()
    const opener = await screen.findByRole('button', { name: /^Record payment for Rent a/ })
    await userEvent.click(opener)
    await userEvent.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(opener).toHaveFocus()
  })

  it('does not switch occurrence when the selected one disappears: says so and cannot save', async () => {
    let gone = false
    responder = () => json(200, summary(gone ? [item('b')] : [item('a'), item('b')]))
    open()
    await userEvent.click(await screen.findByRole('button', { name: /^Record payment for Rent a/ }))
    const form = await screen.findByRole('form', { name: 'Record a payment' })
    const amount = within(form).getByLabelText(/Amount paid/)
    await userEvent.clear(amount)
    await userEvent.type(amount, '7.00')
    gone = true
    await userEvent.click(screen.getByRole('button', { name: 'Refresh' }))
    expect(await within(form).findByText(/is no longer unpaid/, { selector: 'p' })).toBeInTheDocument()
    expect(within(form).getByRole('button', { name: 'Save payment' })).toBeDisabled()
    expect(within(form).getByLabelText('Occurrence')).toHaveValue('a')
    expect(amount).toHaveValue('7.00')
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })
})
