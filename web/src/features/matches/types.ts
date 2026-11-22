// Shapes of /api/v1/payment-matches (OpenAPI PaymentMatch, ADR-0035). Amounts are decimal strings with
// their currency: the UI displays them and never does arithmetic on them (ADR-0005).
import type { Money } from '@/features/month/types'

export type MatchState = 'suggested' | 'confirmed' | 'rejected' | 'reversed'
export type MatchOrigin = 'auto_rule' | 'model_suggestion' | 'user'
export type MatchAction = 'confirm' | 'reject' | 'unmatch' | 'correct' | 'reverse' | 'unconfirm'
/** How a change came (ADR-0045): a signed-in browser, an API token, an API token through MCP, or the server itself. Null before provenance was recorded. */
export type Via = 'session' | 'token' | 'mcp' | 'system'

export interface Decider {
  kind: string
  user_id: string | null
  via?: Via | null
}

/** The bank fact a match refers to, as the provider reported it. description and counterparty are null without banking:read. */
export interface MatchTransaction {
  source: 'bank'
  id: string
  account_id: string
  provider: string
  booking_status: 'booked' | 'pending'
  amount: Money
  date: string
  booking_date: string | null
  value_date: string | null
  description: string | null
  counterparty: string | null
  no_longer_reported_at: string | null
  superseded_by: string | null
  countable: boolean
}

export interface MatchEvent {
  action: string
  state: MatchState
  amount: Money
  by: Decider
  reason: string | null
  at: string
}

export interface PaymentMatch {
  id: string
  occurrence_id: string
  transaction_id: string
  amount: Money
  origin: MatchOrigin
  state: MatchState
  counts_as_evidence: boolean
  needs_review: boolean
  bank_text_redacted: boolean
  created_by: Decider
  decided_by: Decider | null
  decided_at: string | null
  reason: string | null
  transaction: MatchTransaction
  history: MatchEvent[]
  version: number
  occurrence_version?: number
  created_at: string
  updated_at: string
}

/** The occurrence a match pays (GET /occurrences/{id}); only what the review shows. */
export interface OccurrenceLite {
  id: string
  commitment_id: string
  month: string
  due_date: string
  amount: Money
  version: number
  cancelled_at: string | null
  /** The occurrence's derived payment state (only what this screen reads). */
  payment?: {
    status: string
    shortfall_amount?: Money
    /** Where the payment comes from: confirmed bank matches, manual records, and a status set by hand. */
    bank_evidence_count?: number
    manual_evidence_count?: number
    override?: { status: 'paid' | 'unpaid'; applied: boolean } | null
  }
}

export interface MatchingRunResult {
  candidates: number
  confirmed: number
  suggested: number
  dropped: number
  skipped: number
}
