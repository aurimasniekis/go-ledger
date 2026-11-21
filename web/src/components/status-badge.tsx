import type { ComponentType } from 'react'
import {
  CalendarClock,
  CircleCheck,
  CircleDashed,
  Clock,
  Hourglass,
  History,
  LoaderCircle,
  Ban,
  BadgeCheck,
  CircleHelp,
  CloudOff,
  KeyRound,
  Link2Off,
  OctagonX,
  SaveOff,
  TriangleAlert,
  Unplug,
} from 'lucide-react'
import { cn } from '@/lib/utils'

type Entry = { label: string; Icon: ComponentType<{ className?: string }>; tier: Tier; color: string }

// Emphasis tiers, ordered by visual mass so the order survives greyscale:
//  strong  solid fill (needs the user's attention),
//  default tint and a coloured border,
//  quiet   coloured text and icon on a neutral border, no tint.
export type Tier = 'strong' | 'default' | 'quiet'
const TIER_CLASS: Record<Tier, string> = {
  strong: 'border-transparent text-status-strong-foreground font-semibold',
  default: 'border-current font-medium',
  quiet: 'border-border font-medium bg-transparent',
}
const SOLID: Record<string, string> = {
  overdue: 'bg-status-overdue',
  error: 'bg-status-error',
}
const TEXT: Record<string, string> = {
  paid: 'text-status-paid',
  upcoming: 'text-status-upcoming',
  overdue: 'text-status-overdue',
  deferred: 'text-status-deferred',
  stale: 'text-status-stale',
  error: 'text-status-error',
}
const TINT: Record<string, string> = {
  upcoming: 'bg-status-upcoming-bg',
  stale: 'bg-status-stale-bg',
}

// Commitment status: where a commitment stands. Meaning is carried by the icon
// and the text label together; colour only reinforces it (WCAG 1.4.1).
export const COMMITMENT_STATUSES = {
  paid: { label: 'Paid', Icon: CircleCheck, tier: 'quiet', color: 'paid' },
  upcoming: { label: 'Upcoming', Icon: Clock, tier: 'default', color: 'upcoming' },
  // Due and partially paid share the upcoming hue (not yet late); icon and label tell them apart.
  due: { label: 'Due today', Icon: CalendarClock, tier: 'default', color: 'upcoming' },
  partially_paid: { label: 'Partly paid', Icon: CircleDashed, tier: 'default', color: 'upcoming' },
  overdue: { label: 'Overdue', Icon: TriangleAlert, tier: 'strong', color: 'overdue' },
  // A cancelled occurrence (D31) owes nothing: quiet, never the overdue look.
  cancelled: { label: 'Cancelled', Icon: Ban, tier: 'quiet', color: 'deferred' },
  deferred: { label: 'Deferred', Icon: Hourglass, tier: 'quiet', color: 'deferred' },
} satisfies Record<string, Entry>

// Data state: how trustworthy or complete the data behind a row is. A separate
// axis: a data state never replaces a commitment status, and a row may show both.
// Pending-save was added with C4; disconnected and save-failed with U1; the bank connection states with B4. Reserved for
// later work (X4), not yet implemented: bank-pending, needs-review. See docs/design-system.md.
export const DATA_STATES = {
  stale: { label: 'Stale', Icon: History, tier: 'default', color: 'stale' },
  error: { label: 'Error', Icon: OctagonX, tier: 'strong', color: 'error' },
  // A change sent to the server and not yet confirmed: it is not saved.
  pending: { label: 'Not saved yet', Icon: LoaderCircle, tier: 'default', color: 'stale' },
  // The server or a provider cannot be reached: what is shown is the last data that loaded.
  disconnected: { label: 'Disconnected', Icon: Unplug, tier: 'default', color: 'stale' },
  // A change the server did not accept: it was not saved and the earlier value stands.
  save_failed: { label: 'Not saved', Icon: SaveOff, tier: 'strong', color: 'error' },
  // Bank connections (B4). reconnect and temporary are the two failures people confuse: reconnect needs the
  // user to approve access again and retrying cannot help (strong); temporary can be retried and the consent is fine (default).
  reconnect: { label: 'Reconnect needed', Icon: Link2Off, tier: 'strong', color: 'error' },
  temporary: { label: 'Temporary problem', Icon: CloudOff, tier: 'default', color: 'stale' },
  never_fetched: { label: 'Not fetched yet', Icon: CircleHelp, tier: 'quiet', color: 'stale' },
  awaiting: { label: 'Waiting for the bank', Icon: KeyRound, tier: 'default', color: 'stale' },
  // Up to date is a settled, neutral note: it takes no status hue, so a green tick never reads as "paid".
  fresh: { label: 'Up to date', Icon: BadgeCheck, tier: 'quiet', color: 'none' },
} satisfies Record<string, Entry>

export const STATUSES = { ...COMMITMENT_STATUSES, ...DATA_STATES }

export type CommitmentStatus = keyof typeof COMMITMENT_STATUSES
export type DataState = keyof typeof DATA_STATES
export type Status = CommitmentStatus | DataState

export function StatusBadge({ status, label, className }: { status: Status; label?: string; className?: string }) {
  const { label: defaultLabel, Icon, tier, color } = STATUSES[status] as Entry
  return (
    <span
      data-status={status}
      data-tier={tier}
      className={cn(
        'inline-flex items-center gap-1 rounded-sm border px-1.5 py-0.5 text-xs',
        TIER_CLASS[tier],
        tier === 'strong' ? SOLID[color] : TEXT[color],
        tier === 'default' && TINT[color],
        className,
      )}
    >
      <Icon aria-hidden className="size-3.5" />
      {label ?? defaultLabel}
    </span>
  )
}
