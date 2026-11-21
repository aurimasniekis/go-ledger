import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { PaymentPanel, type PaymentPanelProps } from './payment-panel'
import { item, payment } from './fixtures'

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
})
