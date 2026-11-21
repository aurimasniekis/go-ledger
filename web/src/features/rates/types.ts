// Shapes of the exchange-rate routes (internal/httpapi/openapi.yaml: CachedRate, RatesRefreshRun, RatesStatus).
export interface CachedRate {
  provider: string
  source: string
  base: string
  quote: string
  /** The exact rate as the provider published it: shown as text, never turned into a number. */
  value: string
  /** The day the rate is for (YYYY-MM-DD). */
  date: string
  retrieved_at: string
  first_retrieved_at: string
  confirmed_through: string
  stale: boolean
  stale_reason: 'not_retrieved_recently' | 'rate_date_too_old' | null
  previous: { value: string; revised_at: string } | null
}

export interface RatesRun {
  trigger: 'scheduled' | 'manual'
  started_at: string
  finished_at: string
  outcome: 'succeeded' | 'partial' | 'failed'
  currencies_refreshed: number
  error: { kind: string; code: string } | null
}

export interface RatesStatus {
  enabled: boolean
  provider: string
  source: string
  reporting_currency: string | null
  refresh_available: boolean
  refresh_interval_seconds: number
  refresh_after_seconds: number
  stale_after_days: number
  last_run: RatesRun | null
  last_success_at: string | null
  as_of: string
}
