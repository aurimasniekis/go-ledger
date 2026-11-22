import { useEffect, useId, useRef, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Bot, CircleCheck, CircleX, History, Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { StatusBadge } from '@/components/status-badge'
import { Mono } from '@/components/mono'
import { ApiError } from '@/lib/api'
import { formatInstantUtc } from '@/lib/format'
import { holds } from '@/lib/auth'
import { useCommitmentName } from '@/features/matches/api'
import { useMerchant } from '@/features/merchants/api'
import { orgKey } from '@/features/organisation/api'
import type { Definition } from '@/features/organisation/types'
import { ACTION_ERRORS, correctSuggestion, decisionsKey, suggestionAction } from './api'
import type { Suggestion, SuggestionState } from './types'

/** What the screen knows about categories and labels, to name choices and to offer corrections. */
export interface Names {
  categories: Definition[]
  labels: Definition[]
  /** false until every page has loaded: a missing name is then "not loaded yet", not "unknown". */
  complete: boolean
  /** The organisation read failed or is not allowed: names stay as identifiers. */
  unavailable: boolean
}

export const TASK_LABEL = { categorisation: 'Category suggestion', label_suggestion: 'Label suggestion' } as const

const STATE_CHIP: Record<Exclude<SuggestionState, 'superseded'>, { label: string; Icon: typeof Bot }> = {
  pending: { label: 'Needs review', Icon: Bot },
  accepted: { label: 'Accepted', Icon: CircleCheck },
  rejected: { label: 'Rejected', Icon: CircleX },
  reverted: { label: 'Reverted', Icon: Undo2 },
}

/** State is the icon and the words together (never colour alone). A superseded suggestion is stale: a newer one replaced it. */
export function SuggestionStateChip({ state }: { state: SuggestionState }) {
  if (state === 'superseded') return <StatusBadge status="stale" label="Superseded by a newer suggestion" />
  const { label, Icon } = STATE_CHIP[state]
  return (
    <span data-state={state} className="inline-flex items-center gap-1 rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">
      <Icon aria-hidden className="size-3.5" />
      {label}
    </span>
  )
}

function categoryName(c: Definition, all: Definition[]) {
  const parent = c.parent_id ? all.find((x) => x.id === c.parent_id) : undefined
  return parent ? `${parent.name} / ${c.name}` : c.name
}

/** The words for one choice id; an id that is not in the loaded list is said to be unknown, never guessed. */
export function choiceName(id: string, list: Definition[], names: Names, kind: 'category' | 'label'): string {
  const d = list.find((x) => x.id === id)
  if (d) return kind === 'category' ? categoryName(d, list) : d.name
  return names.unavailable || names.complete ? `an archived or unknown ${kind} (${id})` : `a ${kind} (loading its name)`
}

export function ObjectRef({ type, id }: { type: string; id: string }) {
  const commitment = useCommitmentName(type === 'commitment' ? id : undefined)
  const merchant = useMerchant(id, type === 'merchant')
  if (type === 'commitment')
    return (
      <span>
        commitment{' '}
        <Link to="/commitments/$id" params={{ id }} className="underline underline-offset-4">
          {commitment.data ?? <Mono>{id}</Mono>}
        </Link>
      </span>
    )
  if (type === 'merchant')
    return (
      <span>
        merchant{' '}
        <Link to="/merchants/$id" params={{ id }} className="underline underline-offset-4">
          {merchant.data?.display_name ?? <Mono>{id}</Mono>}
        </Link>
      </span>
    )
  if (type === 'bank_connection')
    return (
      <span>
        <Link to="/connections" className="underline underline-offset-4">bank connection</Link> <Mono>{id}</Mono>
      </span>
    )
  return (
    <span>
      {type.replace('_', ' ')} <Mono>{id}</Mono>
    </span>
  )
}

/** Accept, correct and revert write an assignment, so they also need organisation:write (and merchants:write for a merchant). */
function canAssign(grants: readonly string[] | undefined, s: Suggestion) {
  return holds(grants, 'organisation:write') && (s.object_type !== 'merchant' || holds(grants, 'merchants:write'))
}

export interface CardProps {
  suggestion: Suggestion
  names: Names
  grants: readonly string[] | undefined
  offline: boolean
  /** The server answered: say what was saved. */
  onSaved: (message: string) => void
  /** The action was refused or failed: the page says why. It is the page, not this card, that says so, because the reload that follows may remove the card. */
  onFailed: (message: string) => void
}

/**
 * One model suggestion. It is a proposal, never an assignment: the provider and model that made it are shown with it
 * and stay after it is decided. Nothing is saved until the server answers; a refusal says why and changes nothing.
 */
export function SuggestionCard({ suggestion: s, names, grants, offline, onSaved, onFailed }: CardProps) {
  const qc = useQueryClient()
  const titleId = useId()
  const [correcting, setCorrecting] = useState(false)
  const correctBtn = useRef<HTMLButtonElement>(null)
  const restoreFocus = useRef(false)
  // Cancel unmounts the form: focus goes back to the button that opened it, not to the top of the page.
  useEffect(() => {
    if (restoreFocus.current && !correcting) {
      restoreFocus.current = false
      correctBtn.current?.focus()
    }
  }, [correcting])
  const isCategory = s.task === 'categorisation'
  const choiceText = isCategory
    ? s.decision.choice
      ? choiceName(s.decision.choice, names.categories, names, 'category')
      : 'no category'
    : s.decision.choices.length
      ? s.decision.choices.map((c) => choiceName(c, names.labels, names, 'label')).join(', ')
      : 'no labels'
  const nothing = isCategory ? false : s.decision.choices.length === 0

  const run = useMutation({
    mutationFn: (a: { kind: 'accept' | 'reject' | 'revert' } | { kind: 'correct'; answer: Parameters<typeof correctSuggestion>[1] }) =>
      a.kind === 'correct' ? correctSuggestion(s.id, a.answer) : suggestionAction(s.id, a.kind),
    onSuccess: (_saved, a) => {
      setCorrecting(false)
      const what = isCategory ? `category ${choiceText}` : `labels ${choiceText}`
      onSaved(
        a.kind === 'accept'
          ? `Accepted: ${what} is assigned as your own decision. It stays recorded as ${s.provenance}.`
          : a.kind === 'reject'
            ? `Rejected: nothing was assigned. The suggestion (${s.provenance}) stays recorded as rejected.`
            : a.kind === 'revert'
              ? `Reverted: the automatic assignment was taken back (${s.provenance}).`
              : `Corrected: your own answer was assigned instead. The suggestion (${s.provenance}) is recorded as rejected.`,
      )
    },
    // The explanation goes to the page: a refusal usually means the suggestion moved elsewhere, and the reload below then removes this card.
    onError: (e) => onFailed(e instanceof ApiError ? (ACTION_ERRORS[e.code] ?? e.message) : 'Could not reach the server. Nothing was saved.'),
    // Whatever the outcome, the server's list is the truth: reload it.
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: decisionsKey })
      void qc.invalidateQueries({ queryKey: orgKey })
    },
  })
  const busy = run.isPending
  const off = offline || busy
  const assign = canAssign(grants, s)
  const reject = holds(grants, 'decisions:write')
  const revertible = s.state === 'accepted' && s.auto_apply

  return (
    <article aria-labelledby={titleId} className="space-y-2 rounded-md border border-border bg-surface p-3 text-sm">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id={titleId} className="font-semibold">
          {TASK_LABEL[s.task]} for <ObjectRef type={s.object_type} id={s.object_id} />
        </h2>
        <SuggestionStateChip state={s.state} />
      </div>
      <p>
        <span className="text-muted-foreground">{isCategory ? 'Suggested category:' : 'Suggested labels:'}</span> <strong>{choiceText}</strong>
        {s.decision.confidence && <span className="text-muted-foreground"> · the model's confidence: {s.decision.confidence}</span>}
      </p>
      <p className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground" data-testid="provenance">
        <Bot aria-hidden className="size-3.5" />
        <span>{s.provenance}</span>
        {s.suggested_by.label && s.suggested_by.label !== `${s.suggested_by.provider}/${s.suggested_by.model}` && <span>({s.suggested_by.label})</span>}
        <span>
          · made <time dateTime={s.created_at}>{formatInstantUtc(s.created_at)}</time>
        </span>
        {s.decided_at && (
          <span>
            · decided <time dateTime={s.decided_at}>{formatInstantUtc(s.decided_at)}</time>
          </span>
        )}
      </p>
      {s.auto_apply && (
        <p className="text-xs text-muted-foreground">
          {s.state === 'reverted' ? 'It was applied automatically as a model-origin assignment, and has been taken back' : 'Applied automatically as a model-origin assignment: a category you set yourself always wins over it'}
          {s.reversal && ` (reverted ${formatInstantUtc(s.reversal.at)})`}.
        </p>
      )}
      {s.state === 'rejected' && <p className="text-xs text-muted-foreground">Rejected, or replaced by a person's own answer (a correction is recorded as a rejection of this suggestion).</p>}
      {s.state === 'pending' && (
        <p className="text-xs text-muted-foreground">Model output is a proposal. Accepting makes it your own assignment; nothing is assigned until you do. Accept, Correct and Reject are final: they cannot be undone from here.</p>
      )}
      {busy && <StatusBadge status="pending" label="Not saved yet" />}
      {s.state === 'pending' && correcting && (
        <CorrectForm s={s} names={names} busy={off} onCancel={() => { restoreFocus.current = true; setCorrecting(false) }} onSubmit={(answer) => run.mutate({ kind: 'correct', answer })} />
      )}
      {s.state === 'pending' && !correcting && (
        <div className="flex flex-wrap gap-2">
          {assign && (
            <Button size="sm" className="min-h-9" disabled={off || nothing} onClick={() => run.mutate({ kind: 'accept' })}>
              Accept
            </Button>
          )}
          {assign && (
            <Button ref={correctBtn} size="sm" variant="outline" className="min-h-9" disabled={off || names.unavailable} onClick={() => setCorrecting(true)}>
              Correct…
            </Button>
          )}
          {reject && (
            <Button size="sm" variant="outline" className="min-h-9" disabled={off} onClick={() => run.mutate({ kind: 'reject' })}>
              Reject
            </Button>
          )}
          {!assign && !reject && <p className="text-xs text-muted-foreground">Your grants do not allow deciding suggestions.</p>}
          {!assign && reject && <p className="w-full text-xs text-muted-foreground">Accepting or correcting also needs the organisation write grant{s.object_type === 'merchant' ? ' and the merchants write grant' : ''}; you can reject only.</p>}
          {offline && <p className="w-full text-xs text-muted-foreground">You are offline: every action is off until the connection is back.</p>}
        </div>
      )}
      {revertible && assign && (
        <Button size="sm" variant="outline" className="min-h-9" disabled={off} onClick={() => run.mutate({ kind: 'revert' })}>
          <History aria-hidden className="size-4" />
          Revert automatic assignment
        </Button>
      )}
    </article>
  )
}

/** The person's own answer. A category: one of the active categories, or none. Labels: at least one. */
function CorrectForm({ s, names, busy, onCancel, onSubmit }: { s: Suggestion; names: Names; busy: boolean; onCancel: () => void; onSubmit: (a: { categoryId: string | null } | { labelIds: string[] }) => void }) {
  const selectId = useId()
  const firstRef = useRef<HTMLSelectElement>(null)
  const fieldsetRef = useRef<HTMLFieldSetElement>(null)
  // Opening the form moves focus into it.
  useEffect(() => {
    ;(firstRef.current ?? fieldsetRef.current)?.focus()
  }, [])
  const [category, setCategory] = useState<string>(s.decision.choice ?? '')
  const [labels, setLabels] = useState<string[]>([])
  const isCategory = s.task === 'categorisation'
  const cats = names.categories.filter((c) => !c.archived)
  const submitOk = isCategory || labels.length > 0
  return (
    <form
      className="space-y-2 rounded-md border border-border-control p-3"
      onSubmit={(e) => {
        e.preventDefault()
        if (!submitOk) return
        onSubmit(isCategory ? { categoryId: category || null } : { labelIds: labels })
      }}
    >
      {isCategory ? (
        <div className="space-y-1">
          <label htmlFor={selectId} className="font-medium">Your category</label>
          <select ref={firstRef} id={selectId} className="block min-h-10 w-full max-w-xs rounded-md border border-border-control bg-background px-2" value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">No category</option>
            {cats.map((c) => (
              <option key={c.id} value={c.id}>{categoryName(c, names.categories)}</option>
            ))}
          </select>
        </div>
      ) : (
        <fieldset ref={fieldsetRef} tabIndex={-1} className="space-y-1">
          <legend className="font-medium">Your labels</legend>
          {names.labels.filter((l) => !l.archived).map((l) => (
            <label key={l.id} className="flex min-h-8 items-center gap-2">
              <input type="checkbox" checked={labels.includes(l.id)} onChange={(e) => setLabels((cur) => (e.target.checked ? [...cur, l.id] : cur.filter((x) => x !== l.id)))} />
              {l.name}
            </label>
          ))}
          {!submitOk && <p className="text-xs text-muted-foreground">Choose at least one label.</p>}
        </fieldset>
      )}
      <p className="text-xs text-muted-foreground">Saving assigns your answer as your own decision, even if it is the same as the model's, and records this suggestion as rejected. This cannot be undone from here.</p>
      <div className="flex flex-wrap gap-2">
        <Button type="submit" size="sm" className="min-h-9" disabled={busy || !submitOk}>Save my answer</Button>
        <Button type="button" size="sm" variant="ghost" className="min-h-9" onClick={onCancel}>Cancel</Button>
      </div>
    </form>
  )
}
