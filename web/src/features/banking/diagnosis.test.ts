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
