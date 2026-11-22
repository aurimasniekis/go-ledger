import { Link } from '@tanstack/react-router'
import { Amount, DateText } from '@/components/formatted'
import { useManualPayments } from './api'
import { ReplacedMarker } from './provenance'

/**
 * The manual records of an occurrence that a bank match replaced (D45, ADR-0049): listed apart from the payments that count,
 * with the reason they do not. The reason depends on whether a bank payment still stands in for them (bankEvidenceCount):
 * after a Reverse the records stay replaced but nothing replaced them any more. Nothing shows while they load or when the
 * list cannot be read: the row is complete without it.
 */
export function ReplacedRecords({ occurrenceId, bankEvidenceCount, enabled = true, unmatchLink = false }: { occurrenceId: string; bankEvidenceCount: number; enabled?: boolean; unmatchLink?: boolean }) {
  const q = useManualPayments(occurrenceId, enabled)
  const replaced = (q.data ?? []).filter((p) => p.replaced_by_match_id)
  if (replaced.length === 0) return null
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
        {bankEvidenceCount > 0
          ? `${one ? 'This record was' : 'These records were'} replaced by a matched bank payment. ${one ? 'It is' : 'They are'} kept in history and not counted, so the payment is not counted twice.`
          : `The bank payment that replaced ${one ? 'it was' : 'them was'} reversed; ${one ? 'this record stays' : 'these records stay'} in history and ${one ? 'does' : 'do'} not count. Record a new payment if you paid again.`}
      </p>
      {unmatchLink && bankEvidenceCount > 0 && (
        <p className="text-xs text-muted-foreground">
          <Link to="/matches" className="underline underline-offset-4">Unmatch it on Payment matches</Link> to bring {one ? 'it' : 'them'} back.
        </p>
      )}
    </div>
  )
}
