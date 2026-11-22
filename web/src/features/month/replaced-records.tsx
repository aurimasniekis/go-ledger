import { History } from 'lucide-react'
import { Link } from '@tanstack/react-router'
import { useQueries } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { Amount, DateText } from '@/components/formatted'
import { useManualPayments } from './api'
import { ReplacedMarker } from './provenance'

/**
 * The manual records of an occurrence that a bank match replaced (D45, ADR-0049): listed apart from the payments that count,
 * with the reason they do not. The reason depends on the state of the match that replaced each record (replaced_by_match_id),
 * not on the occurrence's bank evidence: after a Reverse the records stay replaced and nothing stands in for them, even if a
 * later, different bank match counts. While a match state is unknown the reason is left out. Nothing shows while they load or when the
 * list cannot be read: the row is complete without it.
 */
type MatchInfo = { state: string; counts_as_evidence?: boolean }
type Standing = 'standing' | 'held' | 'reversed' | 'unknown'

/** confirmed + counting = a bank payment stands in; confirmed + not counting = held (ADR-0049 amendment A); reversed = nothing stands in. */
function standingOf(m: MatchInfo | undefined): Standing {
  if (!m) return 'unknown'
  if (m.state === 'confirmed') return m.counts_as_evidence ? 'standing' : 'held'
  if (m.state === 'reversed') return 'reversed'
  return 'unknown'
}

function HeldMarker() {
  return (
    <span
      data-provenance="held"
      title="A payment you recorded by hand: it counts again because the bank no longer reports the payment that replaced it"
      className="inline-flex items-center gap-1 rounded-sm border border-solid border-border px-1.5 py-0.5 text-xs font-normal text-foreground"
    >
      <History aria-hidden className="size-3.5" />
      Manual record counts again
    </span>
  )
}

export function ReplacedRecords({ occurrenceId, enabled = true, unmatchLink = false }: { occurrenceId: string; enabled?: boolean; unmatchLink?: boolean }) {
  const q = useManualPayments(occurrenceId, enabled)
  const replaced = (q.data ?? []).filter((p) => p.replaced_by_match_id)
  const matchIds = [...new Set(replaced.map((p) => p.replaced_by_match_id as string))]
  const matches = useQueries({
    queries: matchIds.map((id) => ({
      queryKey: ['payment-matches', 'state', id],
      queryFn: ({ signal }: { signal: AbortSignal }) => apiFetch<MatchInfo>(`/api/v1/payment-matches/${encodeURIComponent(id)}`, { signal }),
    })),
  })
  if (replaced.length === 0) return null
  const groups = matchIds.map((id, i) => ({
    id,
    standing: standingOf(matches[i]?.data),
    records: replaced.filter((p) => p.replaced_by_match_id === id),
  }))
  return (
    <div data-testid="replaced-records" className="space-y-2 text-sm">
      {groups.map((g) => {
        const one = g.records.length === 1
        return (
          <div key={g.id} data-testid="replaced-group" data-standing={g.standing} className="space-y-1">
            <ul className="space-y-1">
              {g.records.map((p) => (
                <li key={p.id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
                  {g.standing === 'held' ? <HeldMarker /> : <ReplacedMarker />}
                  <span className="text-muted-foreground">
                    <Amount className="font-mono" amount={p.amount.amount} currency={p.amount.currency} /> paid on <DateText value={p.paid_on} />
                  </span>
                </li>
              ))}
            </ul>
            {g.standing === 'standing' && (
              <p className="text-xs text-muted-foreground">
                {one ? 'This record was' : 'These records were'} replaced by a matched bank payment. {one ? 'It is' : 'They are'} kept in history and not counted, so the payment is not counted twice.
              </p>
            )}
            {g.standing === 'held' && (
              <p className="text-xs text-muted-foreground">
                {one ? 'This record counts' : 'These records count'} again because the bank no longer reports the payment that replaced {one ? 'it' : 'them'}. Review the match on Payment matches.
              </p>
            )}
            {g.standing === 'reversed' && (
              <p className="text-xs text-muted-foreground">
                The bank payment that replaced {one ? 'it was' : 'them was'} reversed; {one ? 'this record stays' : 'these records stay'} in history and {one ? 'does' : 'do'} not count. Record a new payment if you paid again.
              </p>
            )}
            {unmatchLink && g.standing === 'standing' && (
              <p className="text-xs text-muted-foreground">
                <Link to="/matches" className="underline underline-offset-4">Unmatch it on Payment matches</Link> to bring {one ? 'it' : 'them'} back.
              </p>
            )}
          </div>
        )
      })}
    </div>
  )
}
