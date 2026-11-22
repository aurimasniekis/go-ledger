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
export function ReplacedRecords({ occurrenceId, enabled = true, unmatchLink = false }: { occurrenceId: string; enabled?: boolean; unmatchLink?: boolean }) {
  const q = useManualPayments(occurrenceId, enabled)
  const replaced = (q.data ?? []).filter((p) => p.replaced_by_match_id)
  const matches = useQueries({
    queries: replaced.map((p) => ({
      queryKey: ['payment-matches', 'state', p.replaced_by_match_id],
      queryFn: ({ signal }: { signal: AbortSignal }) => apiFetch<{ state: string }>(`/api/v1/payment-matches/${encodeURIComponent(p.replaced_by_match_id!)}`, { signal }),
    })),
  })
  if (replaced.length === 0) return null
  const states = matches.map((m) => m.data?.state)
  const known = states.every((x) => x !== undefined)
  const standing = states.some((x) => x === 'confirmed')
  const one = replaced.length === 1
  return (
    <div data-testid="replaced-records" className="space-y-1 text-sm">
      <ul className="space-y-1">
        {replaced.map((p) => (
          <li key={p.id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <ReplacedMarker />
            <span className="text-muted-foreground">
              <Amount className="font-mono" amount={p.amount.amount} currency={p.amount.currency} /> paid on <DateText value={p.paid_on} />
            </span>
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        {!known ? null : standing
          ? `${one ? 'This record was' : 'These records were'} replaced by a matched bank payment. ${one ? 'It is' : 'They are'} kept in history and not counted, so the payment is not counted twice.`
          : `The bank payment that replaced ${one ? 'it was' : 'them was'} reversed; ${one ? 'this record stays' : 'these records stay'} in history and ${one ? 'does' : 'do'} not count. Record a new payment if you paid again.`}
      </p>
      {unmatchLink && standing && (
        <p className="text-xs text-muted-foreground">
          <Link to="/matches" className="underline underline-offset-4">Unmatch it on Payment matches</Link> to bring {one ? 'it' : 'them'} back.
        </p>
      )}
    </div>
  )
}
