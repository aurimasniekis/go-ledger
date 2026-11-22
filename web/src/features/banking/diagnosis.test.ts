import { describe, expect, it } from 'vitest'
import { accountState, diagnose } from './diagnosis'
import { account, connection, outage, reconnectRequired, run } from './fixtures'
import { isSafeBankUrl } from './redirect'

describe('diagnose', () => {
  it('keeps reconnect and temporary failures apart', () => {
    expect(diagnose(reconnectRequired()).kind).toBe('reconnect')
    expect(diagnose(outage()).kind).toBe('temporary')
    const rate = connection({ last_refresh_run: run({ outcome: 'failed', error: { kind: 'rate_limited', code: 'RATE' } }) })
    expect(diagnose(rate)).toMatchObject({ kind: 'temporary', rateLimited: true })
    const perm = connection({ last_refresh_run: run({ outcome: 'failed', error: { kind: 'permanent', code: 'BAD' } }) })
    expect(diagnose(perm).kind).toBe('rejected')
  })
  it('reads the next automatic attempt only from a waiting schedule', () => {
    expect(diagnose(outage())).toMatchObject({ nextAttemptAt: '2026-11-21T11:00:00Z' })
    expect(diagnose(outage({ schedule: { state: 'waiting', next_attempt_at: '2026-11-21T15:00:00Z', consecutive_failures: 0 } }))).toMatchObject({ nextAttemptAt: null })
  })
  it('a reconnect error on an active connection still asks for a reconnect, never a retry', () => {
    const c = connection({ last_refresh_run: run({ outcome: 'failed', error: { kind: 'reconnect_required', code: 'X' } }) })
    expect(diagnose(c).kind).toBe('reconnect')
  })
  it('after a reconnect the old reconnect error no longer asks for a reconnect, and the data is not called fresh', () => {
    const stale = reconnectRequired()
    // Server state right after a successful reconnect: active again, consent newer than the failed run.
    const c = { ...stale, status: 'active' as const, needs_reconnect: false, consent_granted_at: '2026-11-21T10:00:00Z' }
    expect(diagnose(c)).toEqual({ kind: 'reconnected', lastRefreshedAt: '2026-11-21T08:00:00Z' })
    expect(accountState(c.accounts[0]!, true)).toBe('stale')
    expect(accountState(c.accounts[0]!)).toBe('reconnect')
    // A reconnect error from a run after the consent still asks for a reconnect.
    expect(diagnose({ ...c, consent_granted_at: '2026-11-21T07:00:00Z' }).kind).toBe('reconnect')
    // Once a refresh after the consent succeeds, it is plain ok.
    expect(diagnose({ ...c, last_refresh_run: run({ started_at: '2026-11-21T10:05:00Z' }) })).toEqual({ kind: 'ok', partial: false })
    // Never refreshed before the reconnect: says so.
    expect(diagnose({ ...c, accounts: [] })).toEqual({ kind: 'reconnected', lastRefreshedAt: null })
  })
  it('maps statuses that are not active', () => {
    expect(diagnose(connection({ status: 'pending' })).kind).toBe('pending')
    expect(diagnose(connection({ status: 'failed' })).kind).toBe('consent_failed')
    expect(diagnose(connection({ status: 'disconnected' })).kind).toBe('disconnected')
  })
  it('a partial refresh is ok with a note, a succeeded one is plain ok', () => {
    expect(diagnose(connection({ last_refresh_run: run({ outcome: 'partial', accounts_failed: 1 }) }))).toEqual({ kind: 'ok', partial: true })
    expect(diagnose(connection())).toEqual({ kind: 'ok', partial: false })
  })
})

describe('accountState', () => {
  it('a failed account claims a reconnect only when its error says so', () => {
    const f = (kind: 'retryable' | 'permanent' | 'reconnect_required') =>
      accountState(account({ freshness: { ...account().freshness, state: 'failed', last_error: { kind, code: '' } } }))
    expect(f('retryable')).toBe('temporary')
    expect(f('permanent')).toBe('rejected')
    expect(f('reconnect_required')).toBe('reconnect')
  })
})

describe('isSafeBankUrl', () => {
  it('follows https and loopback http only', () => {
    expect(isSafeBankUrl('https://bank.example/authorize?state=x')).toBe(true)
    expect(isSafeBankUrl('http://127.0.0.1:9000/authorize')).toBe(true)
    expect(isSafeBankUrl('http://bank.example/authorize')).toBe(false)
    expect(isSafeBankUrl('javascript:alert(1)')).toBe(false)
    expect(isSafeBankUrl('not a url')).toBe(false)
  })
})
