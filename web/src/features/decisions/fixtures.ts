import type { Alert, Suggestion } from './types'

export const suggestion = (id: string, over: Partial<Suggestion> = {}): Suggestion => ({
  id: `sg-${id}`,
  task: 'categorisation',
  object_type: 'commitment',
  object_id: 'c-a',
  state: 'pending',
  suggested_by: { provider: 'testprov', model: 'test-model', label: 'Test provider' },
  provenance: 'suggested by testprov/test-model',
  response_id: null,
  decision: { choice: 'cat-groc', choices: [], confidence: 'high' },
  auto_apply: false,
  reversal: null,
  created_at: '2026-11-20T09:00:00Z',
  decided_at: null,
  version: 1,
  ...over,
})

export const alert = (id: string, over: Partial<Alert> = {}): Alert => ({
  id: `al-${id}`,
  kind: 'unmatched_overdue',
  object_type: 'commitment',
  object_id: 'c-a',
  facts: { amount: '120.00 EUR', days_overdue: '4' },
  message: 'Rent is 4 days overdue with no matched payment.',
  model_text: null,
  state: 'open',
  created_at: '2026-11-20T09:00:00Z',
  closed_at: null,
  version: 1,
  ...over,
})

export const def = (id: string, name: string, kind: 'category' | 'label', parent: string | null = null) => ({
  id, kind, name, parent_id: parent, archived: false, archived_at: null, version: 1, created_at: '2026-11-01T00:00:00Z', updated_at: '2026-11-01T00:00:00Z',
})
