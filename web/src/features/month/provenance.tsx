import type { ComponentType } from 'react'
import { Hand, Landmark, PenLine } from 'lucide-react'
import type { MonthItem } from './types'

export type Provenance = 'bank' | 'manual' | 'both' | 'override'

const MARKERS: Record<Provenance, { label: string; Icon: ComponentType<{ className?: string }>; hint: string }> = {
  bank: { label: 'Bank match', Icon: Landmark, hint: 'Settled by a matched bank transaction' },
  both: { label: 'Bank match and manual payment', Icon: Landmark, hint: 'Settled by a matched bank transaction together with a payment you recorded by hand' },
  manual: { label: 'Manual payment', Icon: Hand, hint: 'Settled by a payment you recorded by hand' },
  override: { label: 'Set by hand', Icon: PenLine, hint: 'The status was set by hand, with a reason, over what the payments show' },
}

/**
 * Where a row's settled state came from: bank evidence, a payment recorded by hand, or a hand-set status.
 * It is a separate axis from the status badge (neutral, no status hue), so a bank fact, a user correction and
 * a manual payment never look alike. The label is text; the icon only reinforces it.
 */
export function ProvenanceMarker({ kind }: { kind: Provenance }) {
  const { label, Icon, hint } = MARKERS[kind]
  return (
    <span
      data-provenance={kind}
      title={hint}
      className="inline-flex items-center gap-1 rounded-sm border border-border-control px-1.5 py-0.5 text-xs font-medium text-foreground"
    >
      <Icon aria-hidden className="size-3.5" />
      {label}
    </span>
  )
}

/**
 * What a row shows. An applied override wins (it is the user's correction over the payments). Otherwise the
 * kinds of evidence decide, from the server's per-kind counts: bank matches, manual payments, or both. Evidence
 * of an unknown kind (counted but in neither count) shows no marker rather than a wrong one.
 */
export function provenanceOf(i: MonthItem): Provenance | null {
  const p = i.payment
  if (p.override?.applied) return 'override'
  const bank = p.bank_evidence_count > 0
  const manual = p.manual_evidence_count > 0
  if (bank && manual) return 'both'
  if (bank) return 'bank'
  if (manual) return 'manual'
  return null
}
