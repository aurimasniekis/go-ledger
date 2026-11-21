// Shapes of GET /api/v1/occurrences/summary (OpenAPI MonthSummary). Amounts are
// decimal strings with their currency: the UI displays them and never does
// arithmetic on them (ADR-0005).

export interface Money {
  amount: string
  currency: string
}

export type PaymentStatus = 'paid' | 'partially_paid' | 'upcoming' | 'due' | 'overdue' | 'cancelled'

export interface PaymentState {
  status: PaymentStatus
  derived_status: PaymentStatus
  timing: PaymentStatus | null
  paid_amount: Money
  outstanding_amount: Money
  overpaid_amount: Money
  unconverted_currencies: string[]
  evidence_count: number
  grace_until: string | null
  within_grace: boolean
  override: {
    status: 'paid' | 'unpaid'
    reason: string
    set_at: string
    applied: boolean
  } | null
}

export interface MonthItem {
  id: string
  commitment_id: string
  commitment_name: string
  month: string
  due_date: string
  amount: Money
  version: number
  payment: PaymentState
}

export interface MonthTotals {
  currency: string
  due: Money
  paid: Money
  outstanding: Money
  overdue: Money
  overpaid: Money
  occurrence_count: number
  overdue_count: number
  unconverted_evidence_count: number
}

export interface MonthSummary {
  month: string
  as_of: string
  items: MonthItem[]
  totals: MonthTotals[]
  freshness: {
    computed_at: string
    occurrences_changed_at: string | null
    bank: { available: boolean; note: string }
  }
}

export interface ManualPayment {
  id: string
  occurrence_id: string
  amount: Money
  paid_on: string
  version: number
}
