import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { PaymentPanel, UndoNotice, type PaymentPanelProps } from './payment-panel'
import { item, payment } from './fixtures'
import { manualPaymentsKey } from './api'
import type { ManualPayment } from './types'

function panel(over: Partial<PaymentPanelProps> & { status: 'paid' | 'overdue'; shortfall: string }) {
  const it = item('a', {
    payment: payment({
      status: over.status, derived_status: over.status, timing: over.status === 'paid' ? null : 'overdue',
      outstanding_amount: { amount: over.status === 'paid' ? '0.00' : '0.30', currency: 'EUR' },
      shortfall_amount: { amount: over.shortfall, currency: 'EUR' },
    }),
  })
  const props: PaymentPanelProps = {
    month: '2026-11', asOf: '2026-11-21', items: [], initialId: 'a', initialItem: it, chooser: false,
    onClose: vi.fn(), onSaved: vi.fn(), ...over,
  }
  render(
    <QueryClientProvider client={new QueryClient()}>
      <PaymentPanel {...props} />
    </QueryClientProvider>,
  )
}

describe('PaymentPanel shortfall note', () => {
  it('shows the shortfall, formatted as the month row formats it, for a paid occurrence', () => {
    panel({ status: 'paid', shortfall: '0.50' })
    const note = screen.getByText(/Paid within the commitment's tolerance/)
    expect(note.textContent).toMatch(/0\.50\sEUR.*short/)
  })

  it('shows no shortfall note for an occurrence that is not paid, even when a shortfall is kept', () => {
    panel({ status: 'overdue', shortfall: '0.30' })
    expect(screen.queryByText(/Paid within the commitment's tolerance/)).toBeNull()
  })

  it('shows no note for a paid occurrence with a zero shortfall', () => {
    panel({ status: 'paid', shortfall: '0.00' })
    expect(screen.queryByText(/Paid within the commitment's tolerance/)).toBeNull()
  })

  it('groups thousands with a narrow no-break space (U+202F), as the month row does', () => {
    panel({ status: 'paid', shortfall: '1234.50' })
    const note = screen.getByText(/Paid within the commitment's tolerance/)
    expect(note.textContent).toContain('1\u202F234.50')
    expect(note.textContent).not.toContain('1 234.50') // no ordinary space
  })
})

describe('UndoNotice', () => {
  it('Undo marks the occurrence\'s manual-payments list stale, so a match card does not keep showing the deleted record', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
    const client = new QueryClient()
    client.setQueryData(manualPaymentsKey('occ-a'), [])
    const record = { id: 'mp1', occurrence_id: 'occ-a', amount: { amount: '10.00', currency: 'EUR' }, paid_on: '2026-11-20', note: null, version: 1, created_at: '2026-11-20T08:00:00Z', updated_at: '2026-11-20T08:00:00Z', replaced_by_match_id: null } as ManualPayment
    render(
      <QueryClientProvider client={client}>
        <UndoNotice payment={record} item={item('a')} month="2026-11" onDone={vi.fn()} />
      </QueryClientProvider>,
    )
    await userEvent.click(screen.getByRole('button', { name: 'Undo this payment' }))
    expect(await screen.findByText('Removed')).toBeInTheDocument()
    expect(client.getQueryState(manualPaymentsKey('occ-a'))?.isInvalidated).toBe(true)
  })
})
