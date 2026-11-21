import { describe, expect, it } from 'vitest'
import { ageLabel, isMonth, isZeroAmount, monthLabel, shiftMonth, utcStamp } from './month'

describe('month helpers', () => {
  it('validates and shifts months across year ends', () => {
    expect(isMonth('2026-11')).toBe(true)
    expect(isMonth('2026-13')).toBe(false)
    expect(isMonth(undefined)).toBe(false)
    expect(shiftMonth('2026-01', -1)).toBe('2025-12')
    expect(shiftMonth('2026-12', 1)).toBe('2027-01')
    expect(shiftMonth('2026-11', 14)).toBe('2028-01')
  })
  it('labels and stamps deterministically', () => {
    expect(monthLabel('2026-02')).toBe('February 2026')
    expect(utcStamp('2026-11-21T12:00:05Z')).toBe('2026-11-21 12:00 UTC')
  })
  it('words ages', () => {
    expect(ageLabel(0, 30_000)).toBe('just now')
    expect(ageLabel(0, 60_000)).toBe('1 minute ago')
    expect(ageLabel(0, 3 * 3600_000)).toBe('3 hours ago')
    expect(ageLabel(0, 2 * 86400_000)).toBe('2 days ago')
  })
  it('recognises zero amounts by text', () => {
    for (const z of ['0', '0.00', '0.']) expect(isZeroAmount(z)).toBe(true)
    for (const n of ['0.01', '10.00', '00']) expect(isZeroAmount(n)).toBe(false)
  })
})
