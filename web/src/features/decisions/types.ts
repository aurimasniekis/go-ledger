// The decision tasks API (ADR-0067): model suggestions to review and in-app alerts. internal/httpapi/openapi.yaml is the contract.

export type SuggestionTask = 'categorisation' | 'label_suggestion'
/** The API state: reverted is an accepted auto-applied suggestion whose effect was taken back. */
export type SuggestionState = 'pending' | 'accepted' | 'rejected' | 'superseded' | 'reverted'
export type SuggestionObjectType = 'transaction' | 'commitment' | 'account' | 'merchant'

export interface ModelRef {
  provider: string
  model: string
  label: string
}
export interface Suggestion {
  id: string
  task: SuggestionTask
  object_type: SuggestionObjectType
  object_id: string
  state: SuggestionState
  suggested_by: ModelRef
  /** "suggested by provider/model": shown as the server words it. */
  provenance: string
  response_id: string | null
  decision: { choice: string | null; choices: string[]; confidence: string | null }
  auto_apply: boolean
  reversal: { by: { kind: 'user' | 'token' | 'system'; user_id: string | null }; at: string } | null
  created_at: string
  decided_at: string | null
  version: number
}

export type AlertKind = 'consent_expiring' | 'commitment_amount_changed' | 'unmatched_overdue'
export type AlertState = 'open' | 'dismissed' | 'resolved'
export interface Alert {
  id: string
  kind: AlertKind
  object_type: 'bank_connection' | 'commitment' | 'occurrence'
  object_id: string
  /** Exact values with their currencies, as text. */
  facts: Record<string, string>
  /** Ledger's own wording of the facts. */
  message: string
  /** Optional extra wording from a model, never a replacement for message. */
  model_text: (ModelRef & { text: string; provenance: string }) | null
  state: AlertState
  created_at: string
  closed_at: string | null
  version: number
}
