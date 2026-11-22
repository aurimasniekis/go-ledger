import { useCallback, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Undo2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { StatusBadge } from '@/components/status-badge'
import { DisconnectedState, FailedState, LoadingState } from '@/components/state-panels'
import { Amount, DateText } from '@/components/formatted'
import { useAnnounce } from '@/components/announcer'
import { ApiError, NetworkError } from '@/lib/api'
import { holds, useMe } from '@/lib/auth'
import { isIsoDate } from '@/lib/format'
import { CommitmentForm } from './commitment-form'
import { commitmentKey, createCommitment, deleteCommitment, patchCommitment, useCommitment, useInvalidateCommitments } from './api'
import { changesOccurrences, createBody, describeSchedule, emptyValues, overdueRemovalWarning, patchBody, todayIso, toValues, type FormValues } from './form-model'
import { kindLabel, type Commitment } from './types'

const UNSAVED = 'You have unsaved changes in the form above. Save them or reload the page before ending, undoing or deleting, so they are not lost.'
const NO_WRITE = 'Your role can read commitments but not change them (it lacks commitments:write), so the form is read-only. The server decides what is allowed.'

function Heading({ children, id }: { children: string; id: string }) {
  return (
    <h1 id={id} className="text-2xl font-semibold">
      {children}
    </h1>
  )
}

export function NewCommitmentPage() {
  const me = useMe()
  const nav = useNavigate()
  const invalidate = useInvalidateCommitments()
  const announce = useAnnounce()
  const canWrite = holds(me.data?.grants, 'commitments:write')
  return (
    <section className="space-y-4" aria-labelledby="new-commitment-title">
      <Link to="/commitments" className="text-sm underline">
        All commitments
      </Link>
      <Heading id="new-commitment-title">New commitment</Heading>
      <p className="text-sm text-muted-foreground">
        A commitment is a recurring obligation. Its due dates (occurrences) are made from the schedule and appear on the monthly view. Nothing is saved until the server confirms it.
      </p>
      {me.data && !canWrite ? (
        <p role="status" className="text-sm">{NO_WRITE}</p>
      ) : (
        <CommitmentForm
          mode="create"
          initial={emptyValues()}
          readOnly={!me.data}
          onCancel={() => void nav({ to: '/commitments' })}
          onSave={async (v) => {
            const c = await createCommitment(createBody(v))
            announce(`Commitment ${c.name} created.`)
            await invalidate()
            await nav({ to: '/commitments/$id', params: { id: c.id }, search: { created: true } })
          }}
        />
      )}
    </section>
  )
}

/** What an edit does to occurrences, following the server (ADR-0027 point 4 and 6). */
export function occurrenceNote(c: Commitment, v: FormValues): string {
  const p = patchBody(c, v)
  if (Object.keys(p).length === 0) return 'Nothing has changed yet.'
  if (!changesOccurrences(p)) {
    return 'These changes do not touch any occurrence: the name, kind, priority, notes, match tolerance and grace do not affect due dates or amounts.'
  }
  const dates = 'start_date' in p || 'end_date' in p
  return `A changed amount or schedule applies from the date below forward (today by default). Occurrences already marked paid never change. Unpaid ones due before that date keep their old terms, overdue ones included. Unpaid ones on or after it are rewritten to the new terms, or removed when the new terms schedule nothing that month; months with none get the new occurrence.${dates ? ' A changed start or end date is different: whatever the date below, unpaid occurrences due before the new start date or after the new end date are removed (or cancelled when only deleted payments refer to them), overdue ones too. Occurrences with a payment or a status override you set stay.' : ''} Payment records and bank matches stay as they are.`
}

function SavedNote({ children }: { children: string }) {
  return (
    <p role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-border-control bg-surface p-3 text-sm">
      <StatusBadge status="paid" label="Saved" /> {children}
    </p>
  )
}

export function EditCommitmentPage({ id, created }: { id: string; created?: boolean }) {
  const me = useMe()
  const q = useCommitment(id)
  const qc = useQueryClient()
  const nav = useNavigate()
  const invalidate = useInvalidateCommitments()
  const announce = useAnnounce()
  const canWrite = holds(me.data?.grants, 'commitments:write')
  const [reloadKey, setReloadKey] = useState(0)
  const [saved, setSaved] = useState<string | null>(created ? 'Commitment created.' : null)
  const [ended, setEnded] = useState<{ previous: string | null; on: string } | null>(null)
  const [endOpen, setEndOpen] = useState(false)
  const [delOpen, setDelOpen] = useState(false)
  const [dirty, setDirty] = useState(false)
  // Typing again means the "Saved" note no longer describes what is on screen.
  const onDirty = useCallback((d: boolean) => {
    setDirty(d)
    if (d) setSaved(null)
  }, [])

  if (q.isPending) return <LoadingState label="the commitment" />
  if (q.isError && !q.data) {
    if (q.error instanceof ApiError && q.error.status === 404) {
      return (
        <section className="space-y-3">
          <h1 className="text-2xl font-semibold">Commitment not found</h1>
          <p className="text-muted-foreground">It may have been deleted. Nothing was changed.</p>
          <Link to="/commitments" className="underline">All commitments</Link>
        </section>
      )
    }
    return q.error instanceof NetworkError ? (
      <DisconnectedState onRetry={() => void q.refetch()}>The commitment could not be loaded. Nothing was changed.</DisconnectedState>
    ) : (
      <FailedState title="Could not load this commitment" error={q.error} onRetry={() => void q.refetch()} />
    )
  }
  const c = q.data!

  const apply = async (updated: Commitment) => {
    qc.setQueryData(commitmentKey(id), updated)
    await invalidate()
  }

  return (
    <section className="space-y-4" aria-labelledby="edit-commitment-title">
      <Link to="/commitments" className="text-sm underline">
        All commitments
      </Link>
      <Heading id="edit-commitment-title">{c.name}</Heading>
      <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted-foreground" data-testid="commitment-summary">
        <span>{kindLabel(c.kind)}</span>
        <Amount amount={c.amount.amount} currency={c.amount.currency} />
        <span>{describeSchedule(c.schedule)}</span>
        <span>
          from <DateText value={c.start_date} />
          {c.end_date && (
            <>
              {' '}
              to <DateText value={c.end_date} />
            </>
          )}
        </span>
      </p>
      {q.isError && <StatusBadge status="stale" label="Could not refresh; showing the last loaded version" />}
      {saved && <SavedNote>{saved}</SavedNote>}
      {saved === 'Commitment created.' && (
        <p className="text-sm">
          Its occurrences are on the <Link to="/month" search={{ month: c.start_date.slice(0, 7) }} className="underline">monthly view</Link> from the start date.
        </p>
      )}
      {ended && (
        <EndedNotice
          key={ended.on + ended.previous}
          ended={ended}
          commitment={c}
          blocked={dirty}
          onUndone={async (u) => {
            await apply(u)
            setEnded(null)
            setSaved('End date restored.')
          }}
          onDismiss={() => setEnded(null)}
        />
      )}
      {me.data && !canWrite && <p role="status" className="text-sm">{NO_WRITE}</p>}

      <CommitmentForm
        key={`${c.id}:${c.version}:${reloadKey}`}
        mode="edit"
        initial={toValues(c)}
        readOnly={!canWrite}
        occurrenceNote={(v) => occurrenceNote(c, v)}
        consequenceWarning={(v) => overdueRemovalWarning(c, v)}
        onDirtyChange={onDirty}
        onReload={() => {
          void q.refetch().then(() => setReloadKey((k) => k + 1))
        }}
        onCancel={() => void nav({ to: '/commitments' })}
        onSave={async (v) => {
          const body = patchBody(c, v)
          if (Object.keys(body).length === 0) throw new Error('Nothing has changed, so there is nothing to save.')
          const updated = await patchCommitment(id, c.version, body)
          announce('Changes saved.')
          setSaved('Changes saved.')
          setEnded(null)
          await apply(updated)
        }}
      />

      {canWrite && (
        <div className="space-y-3 rounded-md border border-border bg-surface p-3 sm:p-4">
          <h2 className="text-sm font-semibold">Stop this commitment</h2>
          <p className="text-sm text-muted-foreground">
            <strong>End it</strong> sets its end date: unpaid occurrences due after it are removed, or cancelled when only deleted payments refer to them, even when they are overdue. Occurrences with a payment or a status override you set stay. Undo restores the earlier end date and regenerates occurrences only from today on. <strong>Delete it</strong> removes the commitment, its unpaid occurrences and its category, label and bucket assignments; the server refuses when any occurrence has payment evidence, and there is no undo.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button variant="outline" disabled={dirty} onClick={() => setEndOpen(true)}>
              End commitment
            </Button>
            <Button variant="outline" disabled={dirty} onClick={() => setDelOpen(true)}>
              Delete commitment
            </Button>
          </div>
          {dirty && <p role="status" className="text-sm">{UNSAVED}</p>}
        </div>
      )}

      <EndDialog
        open={endOpen}
        onOpenChange={setEndOpen}
        commitment={c}
        onEnd={async (on) => {
          const updated = await patchCommitment(id, c.version, { end_date: on })
          setEnded({ previous: c.end_date, on })
          setSaved(null)
          announce(`${c.name} ends on ${on}.`)
          await apply(updated)
        }}
      />
      <ConfirmDialog
        open={delOpen}
        onOpenChange={setDelOpen}
        title={`Delete ${c.name}?`}
        description="This removes the commitment, its unpaid occurrences and its category, label and bucket assignments from the monthly view and the outlook. If cancelled history remains the server archives the commitment instead, so it is hidden but its history is kept. If any occurrence has payment evidence the server refuses and nothing changes: end the commitment instead. A deletion cannot be undone here."
        confirmLabel="Delete commitment"
        onConfirm={async () => {
          await deleteCommitment(id, c.version)
          announce(`${c.name} deleted.`)
          qc.removeQueries({ queryKey: commitmentKey(id) })
          await invalidate()
          await nav({ to: '/commitments' })
        }}
      />
    </section>
  )
}

function EndDialog({ open, onOpenChange, commitment, onEnd }: { open: boolean; onOpenChange: (o: boolean) => void; commitment: Commitment; onEnd: (on: string) => Promise<void> }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        title={`End ${commitment.name}`}
        description="The last day the commitment applies, inclusive. Unpaid occurrences due after it are removed, or cancelled when only deleted payments refer to them, even when they are overdue. Occurrences with a payment or a status override you set stay."
      >
        {open && <EndForm commitment={commitment} onCancel={() => onOpenChange(false)} onEnd={async (on) => { await onEnd(on); onOpenChange(false) }} />}
      </DialogContent>
    </Dialog>
  )
}

function EndForm({ commitment, onCancel, onEnd }: { commitment: Commitment; onCancel: () => void; onEnd: (on: string) => Promise<void> }) {
  const [on, setOn] = useState(commitment.end_date ?? new Date().toISOString().slice(0, 10))
  const [error, setError] = useState<string>()
  const [fieldError, setFieldError] = useState<string>()
  const [pending, setPending] = useState(false)
  const [confirmed, setConfirmed] = useState(false)
  const [confirmMissing, setConfirmMissing] = useState(false)
  const busy = useRef(false)
  const retro = isIsoDate(on.trim()) && on.trim() < todayIso()
  return (
    <form
      noValidate
      className="space-y-3"
      onSubmit={async (e) => {
        e.preventDefault()
        if (busy.current) return
        setError(undefined)
        setFieldError(undefined)
        if (!isIsoDate(on.trim())) return setFieldError('Enter the date as YYYY-MM-DD.')
        if (on.trim() < commitment.start_date) return setFieldError('The end date cannot be before the start date.')
        if (retro && !confirmed) return setConfirmMissing(true)
        busy.current = true
        setPending(true)
        try {
          await onEnd(on.trim())
        } catch (err) {
          if (err instanceof ApiError && err.errors.some((x) => x.field === 'end_date')) setFieldError(err.errors.find((x) => x.field === 'end_date')!.message)
          else setError(err instanceof Error ? err.message : 'The end date was not saved.')
          setPending(false)
        } finally {
          busy.current = false
        }
      }}
    >
      <FormError message={error} />
      <Field label="End date" name="end-date" hint="YYYY-MM-DD" error={fieldError}>
        {(c) => <Input {...c} value={on} disabled={pending} autoFocus onChange={(e) => setOn(e.target.value)} />}
      </Field>
      {retro && (
        <div role="alert" className="space-y-2 rounded-md border border-status-error bg-status-error-bg p-3 text-sm">
          <p className="font-medium text-status-error">
            {on.trim()} is before today. Unpaid occurrences due after it will be removed, including overdue ones, so they disappear from the monthly view and the outlook. Undo does not bring back occurrences before today.
          </p>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-1 size-4" checked={confirmed} disabled={pending} aria-invalid={confirmMissing && !confirmed ? true : undefined} onChange={(e) => { setConfirmed(e.target.checked); setConfirmMissing(false) }} />
            <span>I understand that these occurrences will be removed.</span>
          </label>
          {confirmMissing && !confirmed && <p className="font-medium text-status-error">Confirm this before ending the commitment.</p>}
        </div>
      )}
      <div className="flex flex-wrap items-center justify-end gap-2">
        <Button type="button" variant="outline" disabled={pending} onClick={onCancel}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? 'Saving…' : 'End commitment'}
        </Button>
        {pending && <StatusBadge status="pending" />}
      </div>
    </form>
  )
}

/** The notice after ending: the end is confirmed by the server, and Undo puts the earlier end date (or none) back, also confirmed by the server. */
function EndedNotice({ ended, commitment, blocked, onUndone, onDismiss }: { ended: { previous: string | null; on: string }; commitment: Commitment; blocked: boolean; onUndone: (c: Commitment) => Promise<void>; onDismiss: () => void }) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  return (
    <div role="status" className="space-y-2 rounded-md border border-border-control bg-surface p-3 text-sm">
      <p>
        <StatusBadge status="paid" label="Saved" /> {commitment.name} now ends on <DateText value={ended.on} />. Unpaid occurrences due after it were removed, overdue ones included; occurrences with a payment or a status override you set stay. Undo restores the end date and regenerates occurrences only from today on, so an overdue one that was removed does not come back.
      </p>
      {blocked && <p>{UNSAVED}</p>}
      <div className="flex flex-wrap items-center gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={pending || blocked}
          onClick={async () => {
            setPending(true)
            setError(undefined)
            try {
              // From today, as the server does by default: removed occurrences from today on come back from the current terms; earlier ones do not.
              await onUndone(await patchCommitment(commitment.id, commitment.version, { end_date: ended.previous }))
            } catch (e) {
              setError(e instanceof Error ? e.message : 'The undo failed.')
              setPending(false)
            }
          }}
        >
          <Undo2 aria-hidden className="size-4" />
          {pending ? 'Undoing…' : ended.previous ? 'Undo (restore the earlier end date)' : 'Undo (run on without an end date)'}
        </Button>
        <Button variant="ghost" size="sm" disabled={pending} onClick={onDismiss}>
          Dismiss
        </Button>
        {pending && <StatusBadge status="pending" label="Undo not confirmed yet" />}
      </div>
      {error && (
        <p role="alert">
          <StatusBadge status="error" label="Undo failed" /> The end date is still set. {error}
        </p>
      )}
    </div>
  )
}
