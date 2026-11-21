import type { MonthItem, MonthSummary, PaymentState } from './types'

const eur = (amount: string) => ({ amount, currency: 'EUR' })

export function payment(over: Partial<PaymentState> = {}): PaymentState {
  return {
    status: 'upcoming', derived_status: 'upcoming', timing: 'upcoming',
    paid_amount: eur('0.00'), outstanding_amount: eur('10.00'), overpaid_amount: eur('0.00'), shortfall_amount: eur('0.00'),
    unconverted_currencies: [], evidence_count: 0, manual_evidence_count: 0, bank_evidence_count: 0, grace_until: null, within_grace: false, override: null,
    ...over,
  }
}

export function item(id: string, over: Partial<MonthItem> = {}): MonthItem {
  return {
    id, commitment_id: 'c-' + id, commitment_name: 'Rent ' + id, month: '2026-11', due_date: '2026-11-25',
    amount: eur('10.00'), version: 1, payment: payment(), ...over,
  }
}

export function summary(items: MonthItem[], over: Partial<MonthSummary> = {}): MonthSummary {
  return {
    month: '2026-11', as_of: '2026-11-21', items,
    totals: items.length
      ? [{ currency: 'EUR', due: eur('20.00'), paid: eur('0.00'), outstanding: eur('20.00'), overdue: eur('10.00'),
          overpaid: eur('0.00'), occurrence_count: items.length, overdue_count: 1, unconverted_evidence_count: 0 }]
      : [],
    freshness: { computed_at: '2026-11-21T12:00:00Z', occurrences_changed_at: '2026-11-01T08:00:00Z',
      bank: { available: false, note: 'Bank data age is not available from this endpoint yet.' } },
    ...over,
  }
}
