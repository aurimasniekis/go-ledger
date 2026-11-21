import type { BankAccount, BankConnection, BankError } from './types'

/**
 * What a connection needs from the user, decided from what the server says and nothing else. The two
 * failures people confuse are kept apart on purpose:
 *  - reconnect: the bank consent ended or was withdrawn. Retrying cannot help; only approving again at the bank does.
 *  - temporary: the bank or provider could not be reached (or asked for a pause). Retrying later can help; the consent is fine.
 * A permanent refusal is a third thing: neither a reconnect nor a retry will change it.
 */
export type Diagnosis =
  | { kind: 'pending' }
  | { kind: 'consent_failed'; error: BankError | null }
  | { kind: 'reconnect'; error: BankError | null }
  | { kind: 'disconnected' }
  | { kind: 'temporary'; error: BankError; rateLimited: boolean; nextAttemptAt: string | null }
  | { kind: 'rejected'; error: BankError }
  | { kind: 'ok'; partial: boolean }

export function diagnose(c: BankConnection): Diagnosis {
  switch (c.status) {
    case 'pending':
      return { kind: 'pending' }
    case 'failed':
      return { kind: 'consent_failed', error: c.last_error }
    case 'reconnect_required':
      return { kind: 'reconnect', error: c.last_error }
    case 'disconnected':
      return { kind: 'disconnected' }
  }
  const run = c.last_refresh_run
  const err = run?.error
  if (err && run.outcome !== 'succeeded') {
    if (err.kind === 'reconnect_required') return { kind: 'reconnect', error: err }
    if (err.kind === 'permanent') return { kind: 'rejected', error: err }
    const next = c.schedule && ['backoff', 'rate_limited', 'budget_exhausted'].includes(c.schedule.state) ? c.schedule.next_attempt_at : null
    return { kind: 'temporary', error: err, rateLimited: err.kind === 'rate_limited', nextAttemptAt: next }
  }
  return { kind: 'ok', partial: run?.outcome === 'partial' }
}

/** What one account's badge says. An account failure never claims a reconnect unless its error kind says so. */
export type AccountState = 'fresh' | 'stale' | 'never' | 'reconnect' | 'temporary' | 'rejected' | 'disconnected'

export function accountState(a: BankAccount): AccountState {
  const f = a.freshness
  switch (f.state) {
    case 'fresh':
      return 'fresh'
    case 'stale':
      return 'stale'
    case 'never_refreshed':
      return 'never'
    case 'reconnect_required':
      return 'reconnect'
    case 'disconnected':
      return 'disconnected'
    case 'failed':
      return f.last_error?.kind === 'permanent' ? 'rejected' : f.last_error?.kind === 'reconnect_required' ? 'reconnect' : 'temporary'
  }
}

export const BALANCE_LABEL: Record<string, string> = { booked: 'Booked', available: 'Available', expected: 'Expected', other: 'Other' }
