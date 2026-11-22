// The organisation API (task O1/O2, ADR-0053): categories (two levels), labels and buckets, and what is assigned to an
// object. internal/httpapi/openapi.yaml is the contract.

export type Kind = 'category' | 'label' | 'bucket'
export const KINDS: readonly Kind[] = ['category', 'label', 'bucket']
export const KIND_PATH: Record<Kind, string> = { category: 'categories', label: 'labels', bucket: 'buckets' }
export const KIND_NAME: Record<Kind, { one: string; many: string }> = {
  category: { one: 'category', many: 'categories' },
  label: { one: 'label', many: 'labels' },
  bucket: { one: 'bucket', many: 'buckets' },
}

/** What an assignment can be made on. A merchant is left out: it has no record of its own yet (decision D-MER). */
export type ObjectType = 'transaction' | 'commitment' | 'account'

export interface Definition {
  id: string
  kind: Kind
  name: string
  parent_id: string | null
  archived: boolean
  archived_at: string | null
  version: number
  created_at: string
  updated_at: string
}

export type Origin = 'user' | 'rule' | 'model'
export interface Actor {
  kind: 'user' | 'token' | 'system'
  user_id: string | null
  via: 'session' | 'token' | 'mcp' | 'system' | null
}
export interface CategoryAssignment {
  /** null: the person explicitly chose no category. */
  category: Definition | null
  origin: Origin
  source: string | null
  by: Actor
  at: string
  inherited: boolean
}
export interface LabelAssignment {
  label: Definition
  origin: Origin
  source: string | null
  by: Actor
  at: string
  inherited: boolean
}
export interface ObjectAssignments {
  object_type: ObjectType | 'merchant'
  object_id: string
  category: CategoryAssignment | null
  labels: LabelAssignment[]
  removed_labels: LabelAssignment[]
  buckets: Definition[]
}
export interface BucketMember {
  object_type: 'commitment' | 'account'
  object_id: string
  added_by: Actor
  added_at: string
}
