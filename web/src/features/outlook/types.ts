// Shapes of GET /api/v1/outlook (OpenAPI Outlook, ADR-0044). Amounts are decimal strings with their currency: the UI
// displays them and never does arithmetic on them (ADR-0005). Nothing here adds currencies together.

import type { Money } from '@/features/month/types'

export interface ExchangeRate {
  provider: string
  source: string
  base: string
  quote: string
  value: string
  date: string
  retrieved_at: string
  confirmed_through: string
}

export interface OutlookCurrency {
  currency: string
  still_due: Money
  overdue: Money
  upcoming: Money
  overdue_count: number
  unconverted_evidence_count: number
  balance: Money | null
  balance_less_still_due: Money | null
  account_count: number
  stale_account_count: number
  oldest_balance_obtained_at: string | null
  /** The months the overdue part comes from, oldest first. */
  overdue_months: { month: string; count: number; amount: Money }[]
  /** The date of the oldest counted balance; null without a balance. */
  balance_as_of: string | null
  /** Payments dated strictly after balance_as_of: a caveat, never netted into the balance. Null without a balance. */
  payments_after_balance: { count: number; amount: Money } | null
  /**
   * The window with each outstanding amount placed in the month it is planned to be paid (deferral plans): total =
   * still_due − deferred_beyond + adjustments. A plan never changes still_due, overdue or upcoming. Absent from older servers.
   */
  planned_outflows?: {
    total: Money
    /** Part of still_due planned into a later month of the window than its own. */
    deferred_in: Money
    /** Part of still_due planned after the window. */
    deferred_beyond: Money
    /** Fees and charges still planned (not removed, not marked paid); not part of still_due. */
    adjustments: Money
    /** Active plans of the window whose date passed while something planned is unpaid. Absent from older servers. */
    missed_count?: number
    /** Outstanding principal of the occurrences behind missed_count. */
    missed_outstanding?: Money
  }
}

export type FreshnessState = 'fresh' | 'stale' | 'failed' | 'reconnect_required' | 'disconnected' | 'never_refreshed'

export interface OutlookAccount {
  id: string
  connection_id: string
  name: string
  institution: string
  currency: string
  balances: { type: 'booked' | 'available' | 'expected' | 'other'; provider_type: string; amount: Money; reference_date: string | null; obtained_at: string }[]
  counted: boolean
  not_counted_reason: 'no_balance' | 'no_longer_reported' | 'unsupported_balance_type' | null
  freshness: {
    state: FreshnessState
    stale: boolean
    last_success_at: string | null
    last_attempt_at: string | null
    last_error_kind: string | null
    last_error_code: string | null
    notice_code: string | null
  }
}

export interface OutlookConverted {
  currency: string
  complete: boolean
  still_due: Money
  overdue: Money
  /** The per-currency planned total converted with the same rate as still_due; other planned figures are per currency only. */
  planned_total?: Money
  balance: Money | null
  balance_less_still_due: Money | null
  balance_account_count: number
  stale_balance_account_count: number
  included: { currency: string; rate: ExchangeRate | null; age_seconds: number | null; from_cache: boolean; refresh_failed: boolean }[]
  excluded: { currency: string; status: 'stale' | 'missing'; reason: string; rate: ExchangeRate | null }[]
}

export interface Outlook {
  as_of: string
  notice: string
  window: { from: string | null; through: string; truncated: boolean; not_recorded_count: number }
  currencies: OutlookCurrency[]
  balances: { shown: boolean; note: string; stale_after_seconds: number | null }
  accounts: OutlookAccount[]
  converted: OutlookConverted | null
  planned_outflows: { included: boolean; note: string }
  freshness: { computed_at: string; occurrences_changed_at: string | null }
}
