// Shapes of GET /api/v1/occurrences/summary (OpenAPI MonthSummary). Amounts are
// decimal strings with their currency: the UI displays them and never does
// arithmetic on them (ADR-0005).

export interface Money {
  amount: string
  currency: string
}

export type PaymentStatus = 'paid' | 'partially_paid' | 'upcoming' | 'due' | 'overdue' | 'not_recorded' | 'cancelled'

export interface PaymentState {
  status: PaymentStatus
  derived_status: PaymentStatus
  timing: PaymentStatus | null
  paid_amount: Money
  outstanding_amount: Money
  overpaid_amount: Money
  /** How far a bank payment confirmed within the commitment's match tolerance fell short (D39 5a); zero otherwise. */
  shortfall_amount: Money
  unconverted_currencies: string[]
  evidence_count: number
  /** Manual payment records and confirmed bank matches; they add up to evidence_count. */
  manual_evidence_count: number
  bank_evidence_count: number
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
    bank: MonthBankFreshness
  }
}

/** What the month view knows about bank data; `available` is true whenever a connection exists, whatever its state. */
export interface MonthBankFreshness {
  available: boolean
  connections: number
  state: 'none' | 'unknown' | 'fresh' | 'stale' | 'never_refreshed' | 'failed' | 'disconnected' | 'reconnect_required'
  last_success_at: string | null
  note: string
}

export interface ManualPayment {
  id: string
  occurrence_id: string
  amount: Money
  paid_on: string
  note?: string | null
  version: number
  /** The confirmed bank match that replaced this record (D45, ADR-0049): kept and listed, but it no longer counts. Null when none. */
  replaced_by_match_id?: string | null
}
