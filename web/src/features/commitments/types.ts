// Shapes of /api/v1/commitments (OpenAPI Commitment). Amounts are decimal strings with their currency: the UI shows and
// sends them as text and never does arithmetic on them (ADR-0005).

export type CommitmentKind = 'service' | 'server' | 'domain' | 'subscription' | 'debt_repayment'
export type CommitmentPriority = 'essential' | 'important' | 'flexible'
export type LeewayKind = 'unknown' | 'none' | 'period' | 'deadline'

export interface MoneyValue {
  amount: string
  currency: string
}

export type Schedule =
  | { version: 1; frequency: 'monthly'; interval: number; day: number }
  | { version: 1; frequency: 'annual'; month: number; day: number }

export interface Leeway {
  kind: LeewayKind
  days: number | null
  until: string | null
  conditions: string | null
  consequences: string | null
  source: string | null
}

export interface Commitment {
  id: string
  name: string
  kind: CommitmentKind
  amount: MoneyValue
  schedule: Schedule
  start_date: string
  end_date: string | null
  priority: CommitmentPriority | null
  notes: string | null
  match_tolerance: MoneyValue | null
  leeway: Leeway | null
  version: number
  created_at: string
  updated_at: string
}

export const KINDS: { value: CommitmentKind; label: string }[] = [
  { value: 'service', label: 'Service' },
  { value: 'server', label: 'Server or VPS' },
  { value: 'domain', label: 'Domain' },
  { value: 'subscription', label: 'Subscription' },
  { value: 'debt_repayment', label: 'Debt repayment' },
]
export const kindLabel = (k: string) => KINDS.find((x) => x.value === k)?.label ?? k
export const PRIORITIES: CommitmentPriority[] = ['essential', 'important', 'flexible']
