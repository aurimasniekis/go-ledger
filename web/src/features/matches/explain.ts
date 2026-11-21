// Plain words for what the matching engine and the API report about a match. Nothing here decides anything:
// it only rewrites stable codes and actor kinds into sentences. Unknown text is shown as it came.
import type { Decider, MatchEvent, PaymentMatch } from './types'

const PART = /^([a-z_]+)(?: \((.*)\))?$/

/** One safeguard code of the engine (ADR-0039) as a sentence. The detail in brackets is the engine's own wording. */
function sentence(code: string, detail: string | undefined): string {
  switch (code) {
    case 'amount_above_expected':
      return `The transaction is larger than the amount due${detail ? ` (${detail.replace('; confirming would overpay', '')})` : ''}; confirming it would overpay.`
    case 'amount_differs':
      return `The amount differs from the amount due${detail ? ` (${detail})` : ''}.`
    case 'pending':
      return 'The bank has not booked this transaction yet; only a booked one can be confirmed.'
    case 'date_outside_window': {
      const m = detail && /^(\d+) days from the due date; at most (\d+)$/.exec(detail)
      return m ? `It is ${m[1]} days from the due date; automatic confirmation allows at most ${m[2]}.` : 'It is too far from the due date for automatic confirmation.'
    }
    case 'several_transactions': {
      const n = detail && /^(\d+) candidate/.exec(detail)
      return `${n ? n[1] : 'Several'} bank transactions could pay this occurrence, so a person has to choose.`
    }
    case 'several_occurrences': {
      const n = detail && /^(\d+) candidate/.exec(detail)
      return `This transaction could pay ${n ? n[1] : 'several'} different occurrences, so a person has to choose.`
    }
    case 'transaction_has_open_match':
      return 'This transaction already has another open match.'
    default:
      return detail ? `${code} (${detail})` : code
  }
}

/** Splits on "; " outside brackets: the engine's own details contain "; " ("9 days from the due date; at most 3"). */
function splitParts(s: string): string[] {
  const out: string[] = []
  let depth = 0
  let from = 0
  for (let i = 0; i < s.length; i++) {
    if (s[i] === '(') depth++
    else if (s[i] === ')') depth = Math.max(0, depth - 1)
    else if (depth === 0 && s.startsWith('; ', i)) {
      out.push(s.slice(from, i))
      from = i + 2
    }
  }
  out.push(s.slice(from))
  return out.filter(Boolean)
}

export interface PlainReason {
  /** Why it was not confirmed, or how it was confirmed, one sentence each. Empty when the reason is not the engine's. */
  lines: string[]
  /** The description-similarity note, always worded as a hint. */
  hint: string | null
  /** A reason written by a person (or text this screen does not know): shown as it is. */
  verbatim: string | null
}

/** The engine's reason string (ADR-0039) in plain words; any other reason is a person's and stays verbatim. */
export function plainReason(reason: string | null): PlainReason {
  if (!reason) return { lines: [], hint: null, verbatim: null }
  const m = /^automatic match (confirmed|not confirmed): (.*)$/.exec(reason)
  if (!m) return { lines: [], hint: null, verbatim: reason }
  let hint: string | null = null
  const lines: string[] = []
  const body = m[2]!.replace(/(?:; )?description similarity (\d+)\/100 \(a hint only\)\s*$/, (_, s: string) => {
    hint = `The description looks ${s}% similar. That is a hint, not proof.`
    return ''
  })
  if (m[1] === 'confirmed') {
    if (body) lines.push(`Confirmed automatically: ${body}.`)
  } else {
    const refused = /^the store refused the automatic confirmation \(store_refused: ([a-z_]+)\)$/.exec(body)
    if (refused) lines.push(`The automatic confirmation was refused (${refused[1]!.replace(/_/g, ' ')}), so a person has to decide.`)
    else for (const part of splitParts(body)) {
      const p = PART.exec(part)
      lines.push(p ? sentence(p[1]!, p[2]) : part)
    }
  }
  return { lines, hint, verbatim: null }
}

/** Who acted, in words. "you" only when the acting user is the signed-in one; a token is never "you". */
export function actorPhrase(d: Decider | null | undefined, me?: string): string {
  if (!d) return 'nobody yet'
  if (d.kind === 'system') return 'the matching engine'
  if (d.kind === 'user') return !d.user_id || !me || d.user_id === me ? 'you' : 'another person'
  if (d.kind === 'token') return 'an API token or MCP client (not this screen)'
  return d.kind
}

/** The neutral marker for who decided, for scanning a list: nothing for a person at this screen. */
export function deciderMarker(m: PaymentMatch): { kind: 'system' | 'token'; label: string } | null {
  const d = m.decided_by ?? m.created_by
  if (d.kind === 'system') return { kind: 'system', label: m.state === 'suggested' ? 'Engine suggestion' : 'Decided by the engine' }
  if (d.kind === 'token') return { kind: 'token', label: 'Via API token or MCP' }
  return null
}

const STATE_VERB: Record<string, string> = { confirmed: 'Confirmed', rejected: 'Rejected', reversed: 'Reversed' }

/** The sentence about who decided a match, or null while it is only a suggestion. */
export function decisionLine(m: PaymentMatch, me?: string): string | null {
  const d = m.decided_by
  if (!d || m.state === 'suggested') return null
  const verb = STATE_VERB[m.state] ?? m.state
  if (d.kind === 'system') return m.state === 'confirmed' ? 'Confirmed automatically by the matching engine, after its safeguards passed.' : `${verb} by the matching engine.`
  return `${verb} by ${actorPhrase(d, me)}.`
}

/** How the match came to exist, apart from who decided it. */
export function originLine(m: PaymentMatch, me?: string): string {
  if (m.origin === 'auto_rule') return 'Found by a matching rule. A similarity score is a hint, never proof.'
  if (m.origin === 'model_suggestion') return 'Suggested by a model. It is never confirmed automatically; only a person can confirm it.'
  return `Created by ${actorPhrase(m.created_by, me)}.`
}

const HISTORY_VERB: Record<string, string> = {
  suggested: 'Suggested',
  confirmed: 'Confirmed',
  rejected: 'Rejected',
  unmatched: 'Unmatched',
  corrected: 'Corrected to',
  reversed: 'Reversed',
}

/** The verb of a history row ("Corrected to", "Confirmed"); the raw action when it is not known. */
export const historyVerb = (e: MatchEvent): string => HISTORY_VERB[e.action] ?? e.action
