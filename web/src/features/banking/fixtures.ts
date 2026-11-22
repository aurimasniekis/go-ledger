import type { BankAccount, BankApplication, BankConnection, BankRefreshRun } from './types'

const T0 = '2026-11-21T08:00:00Z'
const LATER = '2026-11-21T09:30:00Z'

export const account = (over: Partial<BankAccount> = {}): BankAccount => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZA01',
  name: 'Everyday',
  product: 'Current account',
  currency: 'EUR',
  identifier_hint: '0001',
  no_longer_reported: false,
  transactions_through: '2026-11-20',
  freshness: { state: 'fresh', stale: false, last_success_at: T0, last_attempt_at: T0, last_error: null, notice_code: null },
  balances: [
    { type: 'booked', amount: { amount: '1250.40', currency: 'EUR' }, reference_date: '2026-11-20', obtained_at: T0 },
    { type: 'available', amount: { amount: '1180.40', currency: 'EUR' }, reference_date: null, obtained_at: T0 },
  ],
  ...over,
})

export const run = (over: Partial<BankRefreshRun> = {}): BankRefreshRun => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZR01',
  started_at: T0,
  finished_at: T0,
  outcome: 'succeeded',
  error: null,
  accounts_failed: 0,
  transactions_inserted: 3,
  transactions_rejected: 0,
  ...over,
})

export const connection = (over: Partial<BankConnection> = {}): BankConnection => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZC01',
  provider: 'fake',
  application: 'fake-app',
  mode: 'app',
  environment: 'sandbox',
  account_access: 'all',
  institution: { id: 'Fake Bank', name: 'Fake Bank', country: 'FI' },
  status: 'active',
  needs_reconnect: false,
  freshness: 'fresh',
  consent_granted_at: '2026-10-01T08:00:00Z',
  consent_valid_until: '2027-03-30T08:00:00Z',
  consent_expires_soon: false,
  last_error: null,
  accounts: [account()],
  last_refresh_run: run(),
  refresh_running: false,
  refresh_available_at: null,
  schedule: null,
  as_of: LATER,
  version: 3,
  created_at: '2026-10-01T08:00:00Z',
  updated_at: T0,
  ...over,
})

/** A connection whose consent was withdrawn: reconnect required, with the last good data still on it. */
export const reconnectRequired = (over: Partial<BankConnection> = {}): BankConnection =>
  connection({
    status: 'reconnect_required',
    needs_reconnect: true,
    freshness: 'reconnect_required',
    last_error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' },
    last_refresh_run: run({ outcome: 'failed', error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' } }),
    accounts: [
      account({
        freshness: {
          state: 'reconnect_required',
          stale: true,
          last_success_at: T0,
          last_attempt_at: LATER,
          last_error: { kind: 'reconnect_required', code: 'EXPIRED_SESSION' },
          notice_code: null,
        },
      }),
    ],
    ...over,
  })

/** A bank outage: retryable, the consent is fine and the last good data stays. */
export const outage = (over: Partial<BankConnection> = {}): BankConnection =>
  connection({
    freshness: 'failed',
    last_refresh_run: run({ outcome: 'failed', error: { kind: 'retryable', code: 'TIMEOUT' } }),
    schedule: { state: 'backoff', next_attempt_at: '2026-11-21T11:00:00Z', consecutive_failures: 2 },
    accounts: [
      account({
        freshness: {
          state: 'failed',
          stale: true,
          last_success_at: T0,
          last_attempt_at: LATER,
          last_error: { kind: 'retryable', code: 'TIMEOUT' },
          notice_code: null,
        },
      }),
    ],
    ...over,
  })

export const application = (over: Partial<BankApplication> = {}): BankApplication => ({
  provider: 'fake',
  name: 'fake-app',
  mode: 'app',
  environment: 'sandbox',
  account_access: 'all',
  capabilities: { balance_types: ['booked', 'available'], pending_transactions: true, max_consent_validity_days: 180, max_history_days: 730, background_fetches_per_day: 4 },
  ...over,
})
