import type { ComponentType } from 'react'
import { Hand, Landmark, PenLine } from 'lucide-react'
import type { MonthItem } from './types'

export type Provenance = 'bank' | 'manual' | 'override'

const MARKERS: Record<Provenance, { label: string; Icon: ComponentType<{ className?: string }>; hint: string }> = {
  // Reserved for bank-matched payments (matching, X4). No data supplies it yet, so no row derives it today.
  bank: { label: 'Bank match', Icon: Landmark, hint: 'Settled by a matched bank transaction' },
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
 * What a row shows today. An applied override wins (it is the user's correction over the payments).
 * Otherwise any recorded payment counts as manual: every payment is recorded by hand until bank
 * matching exists, so a bank match is never derived here.
 */
export function provenanceOf(i: MonthItem): Provenance | null {
  const p = i.payment
  if (p.override?.applied) return 'override'
  if (p.evidence_count > 0) return 'manual'
  return null
}
