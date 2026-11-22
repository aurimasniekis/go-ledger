// The merchant API (tasks E1, E2, M1; ADR-0051, ADR-0054, ADR-0058). internal/httpapi/openapi.yaml is the contract.
// Merchant names derive from bank text, so every route needs the administrator-only merchants grants.

export type AliasOrigin = 'user' | 'decision' | 'upgrade'

export interface MerchantAlias {
  id: string
  name: string
  /** The name as matched: NFC, white space collapsed, lower case. */
  key: string
  origin: AliasOrigin
  created_at: string
}

/** A merchant record: a stable id, the name to show and the alias names transactions are matched by. */
export interface MerchantRecord {
  id: string
  display_name: string
  aliases: MerchantAlias[]
  archived: boolean
  archived_at: string | null
  /** The merchant this one was merged into, or null. A merged merchant is archived. */
  merged_into: string | null
  version: number
  created_at: string
  updated_at: string
}

/** What a merge moved to the remaining merchant, counted by the server; kept counts assignments that stayed where they were. */
export interface MergeResult {
  merchant: MerchantRecord
  moved: { aliases: number; decisions: number; enrichments: number; categories: number; labels: number }
  kept: number
}

export type MerchantState = 'confirmed' | 'corrected' | 'rejected' | 'reliable' | 'uncertain' | 'ambiguous' | 'none' | 'unavailable' | 'not_looked_up'
export type EnrichmentStatus = 'identified' | 'ambiguous' | 'none' | 'unavailable'
export type Decision = 'confirmed' | 'corrected' | 'rejected'

export interface Enrichment {
  status: EnrichmentStatus
  name: string | null
  /** Opaque. Ledger never fetches it, and neither does this screen. */
  logo_ref: string | null
  source: string
  /** 0 to 1000, an exact integer; null unless identified. */
  confidence_permille: number | null
  candidates: string[]
  retrieved_at: string | null
  last_failed_at: string | null
  merchant_id: string | null
}

export interface MerchantReview {
  transaction_id: string
  decision: Decision
  name: string | null
  merchant_id: string | null
  by: { kind: 'user' | 'token' | 'system'; user_id: string | null; via?: string }
  decided_at: string
  version: number
}

/** The merchant of one bank transaction. With fallback true the client shows its neutral presentation and no name. */
export interface TransactionMerchant {
  transaction_id: string
  state: MerchantState
  merchant_id: string | null
  name: string | null
  logo_ref: string | null
  identity_source: string | null
  fallback: boolean
  needs_review: boolean
  confirmation_outdated: boolean
  review_inherited: boolean
  enrichment: Enrichment | null
  review: MerchantReview | null
}

export interface MerchantSource {
  name: string
  kind: 'local' | 'remote'
  enabled: boolean
  description: string
  sends_off_machine: string[]
  attribution: string | null
  stores_logo_ref: boolean
  retention_hours: number | null
}
