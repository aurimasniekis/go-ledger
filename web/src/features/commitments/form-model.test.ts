import { describe, expect, it } from 'vitest'
import { commitment } from './fixtures'
import { changesOccurrences, createBody, describeSchedule, emptyValues, formFieldOf, patchBody, toValues, validate } from './form-model'

describe('commitment form model', () => {
  it('builds a create body with exact decimal strings and the schedule members of its frequency only', () => {
    const v = { ...emptyValues(), name: ' Hosting ', amount: '1200.500', currency: 'EUR', day: '15', startDate: '2026-11-01', tolerance: '0.50', notes: ' ' }
    expect(createBody(v)).toEqual({
      name: 'Hosting', kind: 'subscription', amount: { amount: '1200.500', currency: 'EUR' }, schedule: { version: 1, frequency: 'monthly', interval: 1, day: 15 },
      start_date: '2026-11-01', end_date: null, priority: null, notes: null, match_tolerance: { amount: '0.50', currency: 'EUR' },
    })
    expect(createBody({ ...v, frequency: 'annual', month: '2', day: '29', tolerance: '' }).schedule).toEqual({ version: 1, frequency: 'annual', month: 2, day: 29 })
  })

  it('validates what a round trip can save and names the field', () => {
    const e = validate({ ...emptyValues(), amount: '0', currency: 'eur', day: '32', startDate: '2026-02-30', endDate: 'x', tolerance: '-1' })
    expect(Object.keys(e).sort()).toEqual(['amount', 'currency', 'day', 'endDate', 'name', 'startDate', 'tolerance'])
    expect(validate({ ...emptyValues(), name: 'A', amount: '12.50', currency: 'EUR', day: '1', startDate: '2026-01-01', endDate: '2025-12-31' }).endDate).toMatch(/before the start/)
    expect(validate({ ...emptyValues(), name: 'A', amount: '12.50', currency: 'EUR', day: '1', startDate: '2026-01-01' })).toEqual({})
  })

  it('patches only what changed, whole nested members, with nulls for what goes away', () => {
    const c = commitment()
    expect(patchBody(c, toValues(c))).toEqual({})
    expect(patchBody(c, { ...toValues(c), name: 'Flat rent', notes: '  hello ' })).toEqual({ name: 'Flat rent', notes: 'hello' })
    expect(patchBody(c, { ...toValues(c), amount: '810.00' })).toEqual({ amount: { amount: '810.00', currency: 'EUR' } })
    expect(patchBody(c, { ...toValues(c), frequency: 'annual', month: '3', day: '9', endDate: '2026-12-31' })).toEqual({
      schedule: { version: 1, frequency: 'annual', month: 3, day: 9, interval: null }, end_date: '2026-12-31',
    })
    // An interval typed while the schedule is annual is not a change.
    expect(patchBody(commitment({ schedule: { version: 1, frequency: 'annual', month: 3, day: 9 } }), { ...toValues(commitment({ schedule: { version: 1, frequency: 'annual', month: 3, day: 9 } })), interval: '5' })).toEqual({})
    expect(patchBody(c, { ...toValues(c), priority: '' })).toEqual({ priority: null })
  })

  it('sends effective_from only with a change to occurrences, and a leeway patch whole', () => {
    const c = commitment()
    expect(patchBody(c, { ...toValues(c), name: 'x', effectiveFrom: '2026-12-01' })).toEqual({ name: 'x' })
    const p = patchBody(c, { ...toValues(c), amount: '1.00', effectiveFrom: '2026-12-01' })
    expect(p.effective_from).toBe('2026-12-01')
    expect(changesOccurrences(p)).toBe(true)
    expect(patchBody(c, { ...toValues(c), leewayKind: 'period', leewayDays: '5' })).toEqual({
      leeway: { kind: 'period', days: 5, until: null, conditions: null, consequences: null, source: null },
    })
    const withLeeway = commitment({ leeway: { kind: 'period', days: 5, until: null, conditions: null, consequences: null, source: null }, match_tolerance: { amount: '0.50', currency: 'EUR' } })
    expect(patchBody(withLeeway, { ...toValues(withLeeway), leewayKind: '', tolerance: '' })).toEqual({ leeway: null, match_tolerance: null })
  })

  it('maps the server field names to form fields and describes schedules', () => {
    expect(formFieldOf('amount.amount')).toBe('amount')
    expect(formFieldOf('schedule.day')).toBe('day')
    expect(formFieldOf('match_tolerance.currency')).toBe('tolerance')
    expect(formFieldOf('nonsense')).toBeUndefined()
    expect(describeSchedule({ version: 1, frequency: 'monthly', interval: 1, day: 2 })).toBe('Monthly on the 2nd')
    expect(describeSchedule({ version: 1, frequency: 'monthly', interval: 3, day: 11 })).toBe('Every 3 months on the 11th')
    expect(describeSchedule({ version: 1, frequency: 'annual', month: 2, day: 29 })).toBe('Yearly on February 29')
  })
})
