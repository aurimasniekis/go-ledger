// The bank connection API (task B4, ADR-0040). Amounts arrive as exact decimal strings and are only displayed.

export type ConnectionStatus = 'pending' | 'active' | 'reconnect_required' | 'failed' | 'disconnected'
export type FreshnessState = 'fresh' | 'stale' | 'failed' | 'reconnect_required' | 'disconnected' | 'never_refreshed'
/** What to do about a failure: only reconnect_required needs a new consent; retrying cannot help it. */
export type BankErrorKind = 'reconnect_required' | 'rate_limited' | 'retryable' | 'permanent' | 'canceled'

export interface BankError {
  kind: BankErrorKind
  code: string
}
export interface BankFreshness {
  state: FreshnessState
  stale: boolean
  last_success_at: string | null
  last_attempt_at: string | null
  last_error: BankError | null
  notice_code: string | null
}
export interface BankBalance {
  type: 'booked' | 'available' | 'expected' | 'other'
  amount: { amount: string; currency: string }
  reference_date: string | null
  obtained_at: string
}
export interface BankAccount {
  id: string
  name: string
  product: string
  currency: string
  identifier_hint: string | null
  no_longer_reported: boolean
  transactions_through: string | null
  freshness: BankFreshness
  balances: BankBalance[]
}
export interface BankRefreshRun {
  id: string
  started_at: string
  finished_at: string | null
  outcome: 'running' | 'succeeded' | 'partial' | 'failed' | 'interrupted'
  error: BankError | null
  accounts_failed: number
  transactions_inserted: number
  transactions_rejected: number
}
export interface BankSchedule {
  state: 'waiting' | 'due' | 'running' | 'backoff' | 'rate_limited' | 'budget_exhausted' | 'not_active' | 'no_provider'
  next_attempt_at: string | null
  consecutive_failures: number
}
export interface BankConnection {
  id: string
  provider: string
  application: string
  mode: 'app' | 'developer'
  environment: 'production' | 'sandbox'
  account_access: 'all' | 'linked_only'
  institution: { id: string; name: string; country: string }
  status: ConnectionStatus
  needs_reconnect: boolean
  freshness: FreshnessState
  consent_granted_at: string | null
  consent_valid_until: string | null
  consent_expires_soon: boolean
  last_error: BankError | null
  accounts: BankAccount[]
  last_refresh_run: BankRefreshRun | null
  /** A refresh is still going on in the background: poll until false. */
  refresh_running: boolean
  schedule: BankSchedule | null
  as_of: string
  version: number
  created_at: string
  updated_at: string
}
export interface BankApplication {
  provider: string
  name: string
  mode: 'app' | 'developer'
  environment: 'production' | 'sandbox'
  account_access: 'all' | 'linked_only'
  capabilities: {
    balance_types: string[]
    pending_transactions: boolean
    max_consent_validity_days: number
    max_history_days: number
    background_fetches_per_day: number
  }
}
export interface BankInstitution {
  id: string
  name: string
  country: string
  max_consent_validity_days: number
}
export interface ConsentStart {
  connection: BankConnection
  authorization_url: string
}
