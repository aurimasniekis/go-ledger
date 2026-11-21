import type { PaymentMatch } from './types'

const eur = (amount: string) => ({ amount, currency: 'EUR' })

export function match(id: string, over: Partial<PaymentMatch> = {}): PaymentMatch {
  return {
    id, occurrence_id: 'occ-' + id, transaction_id: 'tx-' + id, amount: eur('10.00'), origin: 'auto_rule', state: 'suggested',
    counts_as_evidence: false, needs_review: false, bank_text_redacted: false,
    created_by: { kind: 'system', user_id: null }, decided_by: null, decided_at: null,
    reason: 'automatic match not confirmed: pending (only a booked transaction can be confirmed); description similarity 100/100 (a hint only)',
    transaction: {
      source: 'bank', id: 'tx-' + id, account_id: 'acc', provider: 'fake', booking_status: 'booked', amount: eur('-10.00'),
      date: '2026-11-24', booking_date: '2026-11-24', value_date: null, description: 'RENT NOVEMBER', counterparty: 'Landlord Ltd',
      no_longer_reported_at: null, superseded_by: null, countable: true,
    },
    history: [], version: 1, created_at: '2026-11-24T08:00:00Z', updated_at: '2026-11-24T08:00:00Z',
    ...over,
  }
}

export const occurrence = (id: string) => ({
  id: 'occ-' + id, commitment_id: 'c-' + id, month: '2026-11', due_date: '2026-11-25', amount: eur('10.00'), version: 1, cancelled_at: null,
})
