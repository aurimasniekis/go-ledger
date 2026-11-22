import type { CategoryAssignment, Definition, Kind, LabelAssignment, ObjectAssignments } from './types'

const now = '2026-11-21T10:00:00Z'
export const id = (n: string) => '01JD3Y4C8V9M2Q7W5R6T0XZ' + n.padEnd(3, '0').slice(0, 3)

export function def(kind: Kind, n: string, name: string, over: Partial<Definition> = {}): Definition {
  return { id: id(n), kind, name, parent_id: null, archived: false, archived_at: null, version: 1, created_at: now, updated_at: now, ...over }
}

const by = { kind: 'user', user_id: id('USR'), via: 'session' } as const
export function catAssignment(category: Definition | null, over: Partial<CategoryAssignment> = {}): CategoryAssignment {
  return { category, origin: 'user', source: null, by, at: now, inherited: false, ...over }
}
export function labelAssignment(label: Definition, over: Partial<LabelAssignment> = {}): LabelAssignment {
  return { label, origin: 'user', source: null, by, at: now, inherited: false, ...over }
}
export function assignments(over: Partial<ObjectAssignments> = {}): ObjectAssignments {
  return { object_type: 'commitment', object_id: 'c-a', category: null, labels: [], removed_labels: [], buckets: [], ...over }
}
