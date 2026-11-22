import { describe, expect, it } from 'vitest'
import { AMBIGUITY_CODES, actorPhrase, decisionLine, plainReason, wasUnmatched } from './explain'
import { match } from './fixtures'

describe('plainReason keeps the engine code of each sentence', () => {
  it('gives each safeguard its code, so ambiguity can be recognised without reading the wording', () => {
    const r = plainReason('automatic match not confirmed: several_transactions (2 candidate transactions for this occurrence); several_occurrences (3 candidate occurrences for this transaction); date_outside_window (9 days from the due date; at most 3)')
    expect(r.lines.map((l) => l.code)).toEqual(['several_transactions', 'several_occurrences', 'date_outside_window'])
    expect(r.lines.filter((l) => l.code !== null && AMBIGUITY_CODES.includes(l.code)).map((l) => l.text)).toEqual([
      '2 bank transactions could pay this occurrence, so a person has to choose.',
      'This transaction could pay 3 different occurrences, so a person has to choose.',
    ])
  })

  it('codes a confirmation as not a safeguard, and keeps a person\'s reason verbatim', () => {
    expect(plainReason('automatic match confirmed: 10.00 EUR, booked').lines).toEqual([{ code: null, text: 'Confirmed automatically: 10.00 EUR, booked.' }])
    expect(plainReason('rejected in another tab')).toEqual({ lines: [], hint: null, verbatim: 'rejected in another tab' })
  })
})

describe('who acted, in words', () => {
  it('names a token or MCP client acting as the signed-in user "you, via ..."', () => {
    expect(actorPhrase({ kind: 'user', user_id: 'u', via: 'token' }, 'u')).toBe('you, via an API token')
    expect(actorPhrase({ kind: 'user', user_id: 'u', via: 'mcp' }, 'u')).toBe('you, via an MCP client')
    expect(actorPhrase({ kind: 'user', user_id: 'u', via: 'session' }, 'u')).toBe('you')
    expect(actorPhrase({ kind: 'user', user_id: 'v', via: 'mcp' }, 'u')).toBe('another person, via an MCP client')
    expect(actorPhrase({ kind: 'token', user_id: 'u', via: 'token' }, 'u')).toBe('an API token (not this screen)')
  })
})

describe('decisionLine for closed matches', () => {
  const ev = (action: string) => ({ action, state: 'rejected' as const, amount: { amount: '10.00', currency: 'EUR' }, by: { kind: 'user', user_id: 'u' }, reason: null, at: '2026-11-25T09:30:00Z' })
  const by = { kind: 'user', user_id: 'u', via: 'session' as const }
  it('says Unmatched, and that it had counted, only when the latest history action is unmatched', () => {
    const unmatched = match('a', { state: 'rejected', decided_by: by, decided_at: '2026-11-25T09:30:00Z', history: [ev('confirmed'), ev('unmatched')] })
    expect(wasUnmatched(unmatched)).toBe(true)
    expect(decisionLine(unmatched, 'u')).toBe('Unmatched by you (it had counted as payment). Closed 2026-11-25 09:30 UTC.')
    const rejected = match('a', { state: 'rejected', decided_by: by, decided_at: '2026-11-25T09:30:00Z', history: [ev('unmatched'), ev('rejected')] })
    expect(wasUnmatched(rejected)).toBe(false)
    expect(decisionLine(rejected, 'u')).toBe('Rejected by you. Closed 2026-11-25 09:30 UTC.')
    expect(decisionLine(match('a', { state: 'reversed', decided_by: by }), 'u')).toBe('Reversed by you.')
    expect(decisionLine(match('a', { state: 'confirmed', decided_by: by, decided_at: '2026-11-25T09:30:00Z' }), 'u')).toBe('Confirmed by you.')
  })
})
