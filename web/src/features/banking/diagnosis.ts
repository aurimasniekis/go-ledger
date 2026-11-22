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
  | { kind: 'reconnected'; lastRefreshedAt: string | null }
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
    // A reconnect error from a refresh that began before the current consent was granted is superseded: the user has
    // reconnected since. The card says so, offers a refresh, and does not call the data fresh until one succeeds.
    if (err.kind === 'reconnect_required' && c.consent_granted_at && Date.parse(run.started_at) < Date.parse(c.consent_granted_at)) {
      return { kind: 'reconnected', lastRefreshedAt: lastRefreshedAt(c) }
    }
    if (err.kind === 'reconnect_required') return { kind: 'reconnect', error: err }
    if (err.kind === 'permanent') return { kind: 'rejected', error: err }
    const next = c.schedule && ['backoff', 'rate_limited', 'budget_exhausted'].includes(c.schedule.state) ? c.schedule.next_attempt_at : null
    return { kind: 'temporary', error: err, rateLimited: err.kind === 'rate_limited', nextAttemptAt: next }
  }
  return { kind: 'ok', partial: run?.outcome === 'partial' }
}

/** The newest successful refresh across a connection's accounts, or null when none ever succeeded. */
export function lastRefreshedAt(c: BankConnection): string | null {
  let best: string | null = null
  for (const a of c.accounts) {
    const t = a.freshness.last_success_at
    if (t && (best === null || Date.parse(t) > Date.parse(best))) best = t
  }
  return best
}

/** What one account's badge says. An account failure never claims a reconnect unless its error kind says so. */
export type AccountState = 'fresh' | 'stale' | 'never' | 'reconnect' | 'temporary' | 'rejected' | 'disconnected'

/**
 * An account's reconnect_required error is superseded when its last attempt began before the current consent was granted
 * and the connection is active again: the user has reconnected since. Decided per account, so a running refresh or an
 * account that is no longer reported (and keeps its old error) is judged by its own last attempt.
 */
export function reconnectSuperseded(a: BankAccount, c: BankConnection): boolean {
  const f = a.freshness
  if (c.status !== 'active' || !c.consent_granted_at || !f.last_attempt_at) return false
  if (f.state !== 'reconnect_required' && f.last_error?.kind !== 'reconnect_required') return false
  return Date.parse(f.last_attempt_at) < Date.parse(c.consent_granted_at)
}

export function accountState(a: BankAccount, superseded = false): AccountState {
  const f = a.freshness
  // After a reconnect the old "reconnect needed" no longer applies, but nothing has been refreshed yet: stale, never fresh.
  if (superseded) return 'stale'
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
