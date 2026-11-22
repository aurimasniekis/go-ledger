import { Amount, DateText } from '@/components/formatted'
import { useManualPayments } from './api'
import { ReplacedMarker } from './provenance'

/**
 * The manual records of an occurrence that a bank match replaced (D45, ADR-0049): listed apart from the payments that count,
 * with the reason they do not. Nothing shows while they load or when the list cannot be read: the row is complete without it.
 */
export function ReplacedRecords({ occurrenceId, enabled = true }: { occurrenceId: string; enabled?: boolean }) {
  const q = useManualPayments(occurrenceId, enabled)
  const replaced = (q.data ?? []).filter((p) => p.replaced_by_match_id)
  if (replaced.length === 0) return null
  return (
    <div data-testid="replaced-records" className="space-y-1 text-sm">
      <ul className="space-y-1">
        {replaced.map((p) => (
          <li key={p.id} className="flex flex-wrap items-center gap-x-2 gap-y-1">
            <ReplacedMarker />
            <span>
              <Amount className="font-mono" amount={p.amount.amount} currency={p.amount.currency} /> paid on <DateText value={p.paid_on} />
            </span>
          </li>
        ))}
      </ul>
      <p className="text-xs text-muted-foreground">
        {replaced.length === 1 ? 'This record was' : 'These records were'} replaced by a matched bank payment. {replaced.length === 1 ? 'It is' : 'They are'} kept in history and not counted, so the payment is not counted twice.
      </p>
    </div>
  )
}
