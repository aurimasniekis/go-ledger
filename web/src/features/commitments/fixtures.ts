import type { Commitment } from './types'

export const commitment = (over: Partial<Commitment> = {}): Commitment => ({
  id: 'c-rent', name: 'Rent', kind: 'service', amount: { amount: '800.00', currency: 'EUR' },
  schedule: { version: 1, frequency: 'monthly', interval: 1, day: 2 }, start_date: '2026-01-01', end_date: null, priority: 'essential', notes: null,
  match_tolerance: null, leeway: null, version: 3, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', ...over,
})
