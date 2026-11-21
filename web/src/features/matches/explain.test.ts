import { describe, expect, it } from 'vitest'
import { AMBIGUITY_CODES, plainReason } from './explain'

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
