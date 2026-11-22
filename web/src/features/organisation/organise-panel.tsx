import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/input'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { QueryState } from '@/components/query-state'
import { holds, useMe } from '@/lib/auth'
import { DateText } from '@/components/formatted'
import {
  addBucketMember,
  addObjectLabel,
  objectKey,
  orgKey,
  removeBucketMember,
  removeObjectLabel,
  resetObjectCategory,
  setObjectCategory,
  useAllDefinitions,
  useObjectOrganisation,
} from './api'
import { problemText } from './organisation-page'
import type { CategoryAssignment, Definition, LabelAssignment, ObjectAssignments, ObjectType } from './types'

/** Who made an assignment, in words: the person's choice outranks a rule's, which outranks a model's. */
export function originText(a: Pick<CategoryAssignment | LabelAssignment, 'origin' | 'source' | 'inherited'>): string {
  const src = a.source ? `: ${a.source}` : ''
  const base = a.origin === 'user' ? 'Your choice' : a.origin === 'rule' ? `Automatic, by a rule${src}` : `Automatic, by a model${src}`
  return a.inherited ? `${base}, inherited from the pending transaction this booking replaced` : base
}

function Origin({ a }: { a: Pick<CategoryAssignment | LabelAssignment, 'origin' | 'source' | 'inherited' | 'at'> }) {
  return (
    <span className="text-xs text-muted-foreground">
      {originText(a)}, <DateText value={a.at} />
    </span>
  )
}

const ARCHIVED = <span className="ml-1 rounded border border-border px-1 py-0.5 text-xs font-normal text-muted-foreground">Archived</span>

/**
 * The Organise control of one transaction, commitment, bank account or merchant record: a disclosure that reads what is assigned only when it is
 * opened. It is hidden without organisation:read. Every change is the person's choice (origin user) and is shown as saved only
 * after the server has answered.
 */
export function OrganiseDisclosure({ objectType, objectId, subject, locked }: { objectType: ObjectType; objectId: string; subject: string; locked?: string }) {
  const me = useMe()
  const [open, setOpen] = useState(false)
  const panel = useId()
  const grants = me.data?.grants
  // A merchant's assignments are as sensitive as the merchant (bank-derived names): they also need the merchants grants.
  const merchant = objectType === 'merchant'
  if (!holds(grants, 'organisation:read') || (merchant && !holds(grants, 'merchants:read'))) return null
  const canWrite = holds(grants, 'organisation:write') && (!merchant || holds(grants, 'merchants:write')) && !locked
  return (
    <div className="space-y-2">
      <Button variant="ghost" size="sm" className="h-auto min-h-8 py-1 text-foreground underline underline-offset-4" aria-expanded={open} aria-controls={panel} aria-label={`Organise ${subject}`} onClick={() => setOpen(!open)}>
        Organise
      </Button>
      <div id={panel} hidden={!open}>
        {open && <OrganisePanel objectType={objectType} objectId={objectId} subject={subject} canWrite={canWrite} locked={locked} />}
      </div>
    </div>
  )
}

export function OrganisePanel({ objectType, objectId, subject, canWrite, locked }: { objectType: ObjectType; objectId: string; subject: string; canWrite: boolean; locked?: string }) {
  const q = useObjectOrganisation(objectType, objectId, true)
  return (
    <div className="space-y-3 rounded-md border border-border bg-muted/40 p-3 text-sm" aria-label={`Organisation of ${subject}`} role="group">
      {locked && <p className="text-xs text-muted-foreground">{locked}</p>}
      <QueryState query={q} label={`categories, labels and buckets of ${subject}`}>
        {(a) => <Assignments a={a} objectType={objectType} objectId={objectId} subject={subject} canWrite={canWrite} />}
      </QueryState>
    </div>
  )
}

function Assignments({ a, objectType, objectId, subject, canWrite }: { a: ObjectAssignments; objectType: ObjectType; objectId: string; subject: string; canWrite: boolean }) {
  const qc = useQueryClient()
  const [saved, setSaved] = useState<string>()
  const status = useRef<HTMLParagraphElement>(null)
  // The button that was pressed is replaced when the answer arrives, so keyboard focus goes to the status line, which says what was saved.
  useEffect(() => {
    if (saved) status.current?.focus()
  }, [saved])
  const change = useMutation({
    mutationFn: (c: { run: () => Promise<ObjectAssignments | void>; done: string; control: string }) => c.run(),
    onMutate: () => setSaved(undefined),
    onSuccess: async (next, c) => {
      if (next) qc.setQueryData(objectKey(objectType, objectId), next)
      else await qc.invalidateQueries({ queryKey: objectKey(objectType, objectId) })
      void qc.invalidateQueries({ queryKey: [...orgKey, 'members'] })
      setSaved(c.done)
    },
  })
  const run = (done: string, fn: () => Promise<ObjectAssignments | void>, control: string) => change.mutate({ run: fn, done, control })
  const busy = change.isPending
  const pending = busy ? change.variables?.control : undefined
  const bucketable = objectType === 'commitment' || objectType === 'account'

  return (
    <>
      <FormError message={change.isError ? problemText(change.error) : undefined} />
      <p ref={status} tabIndex={-1} role="status" className="text-xs font-medium outline-none focus-visible:ring-2 focus-visible:ring-ring">
        {busy ? 'Saving…' : saved}
      </p>
      <CategoryPart a={a} canWrite={canWrite} busy={busy} pending={pending} subject={subject} run={run} objectType={objectType} objectId={objectId} />
      <LabelsPart a={a} canWrite={canWrite} busy={busy} pending={pending} subject={subject} run={run} objectType={objectType} objectId={objectId} />
      {bucketable && <BucketsPart a={a} canWrite={canWrite} busy={busy} pending={pending} subject={subject} run={run} objectType={objectType} objectId={objectId} />}
    </>
  )
}

interface PartProps {
  a: ObjectAssignments
  canWrite: boolean
  busy: boolean
  /** The control whose change is in flight: only it reads Saving. */
  pending?: string
  subject: string
  objectType: ObjectType
  objectId: string
  run: (done: string, fn: () => Promise<ObjectAssignments | void>, control: string) => void
}

function Part({ title, children }: { title: string; children: ReactNode }) {
  const id = useId()
  return (
    <section aria-labelledby={id} className="space-y-2">
      <h3 id={id} className="font-medium">
        {title}
      </h3>
      {children}
    </section>
  )
}

/** Categories in picker order: each top-level one, then its children, named "Parent › Child". */
function categoryOptions(all: Definition[]): { id: string; label: string }[] {
  const byId = new Map(all.map((c) => [c.id, c]))
  const out: { id: string; label: string }[] = []
  for (const top of all.filter((c) => c.parent_id === null)) {
    out.push({ id: top.id, label: top.name })
    for (const child of all.filter((c) => c.parent_id === top.id)) out.push({ id: child.id, label: `${top.name} › ${child.name}` })
  }
  for (const c of all) if (c.parent_id && !byId.has(c.parent_id)) out.push({ id: c.id, label: c.name })
  return out
}

const NONE = '__none__'

function CategoryPart({ a, canWrite, busy, pending, subject, run, objectType, objectId }: PartProps) {
  const cats = useAllDefinitions('category', 'false', canWrite)
  const allCats = useAllDefinitions('category', undefined)
  const cur = a.category
  const [choice, setChoice] = useState('')
  const nameOf = (d: Definition) => {
    const p = d.parent_id ? allCats.items.find((c) => c.id === d.parent_id) : undefined
    return p ? `${p.name} › ${d.name}` : d.name
  }
  const currentValue = cur ? (cur.category ? cur.category.id : NONE) : ''
  return (
    <Part title="Category">
      {cur === null ? (
        <p>No category yet. A rule or model may assign one; choosing here makes it your choice, which automatic assignment never changes.</p>
      ) : (
        <p>
          {cur.category ? (
            <>
              <strong>{nameOf(cur.category)}</strong>
              {cur.category.archived && ARCHIVED}
            </>
          ) : (
            <strong>No category (you chose none)</strong>
          )}
          <br />
          <Origin a={cur} />
        </p>
      )}
      {cur && cur.origin !== 'user' && <p className="text-xs text-muted-foreground">This was assigned automatically. Choose a category below to override it: your choice then stays until you hand it back.</p>}
      {canWrite && (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={`Category for ${subject}`} name="category" className="min-w-48 grow sm:max-w-xs">
            {(p) => (
              <Select {...p} value={choice} disabled={busy} onChange={(e) => setChoice(e.target.value)}>
                <option value="">Choose…</option>
                <option value={NONE}>No category</option>
                {categoryOptions(cats.items).map((o) => (
                  <option key={o.id} value={o.id}>
                    {o.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Button
            size="sm"
            disabled={busy || choice === '' || choice === currentValue && cur?.origin === 'user'}
            onClick={() => {
              const target = choice === NONE ? null : choice
              const label = target ? (categoryOptions(cats.items).find((o) => o.id === target)?.label ?? 'the category') : 'no category'
              run(`Saved: ${label} is your choice for ${subject}.`, () => setObjectCategory(objectType, objectId, target), 'category')
              setChoice('')
            }}
          >
            {pending === 'category' ? 'Saving…' : 'Set category'}
          </Button>
          {cur && cur.origin === 'user' && !cur.inherited && (
            <Button variant="outline" size="sm" disabled={busy} onClick={() => run(`Saved: ${subject} is back to automatic assignment.`, () => resetObjectCategory(objectType, objectId), 'reset')}>
              Hand back to automatic
            </Button>
          )}
        </div>
      )}
      {cur?.inherited && canWrite && <p className="text-xs text-muted-foreground">This is inherited from the pending transaction, so it cannot be reset here: reset it on that transaction, or choose a category to replace it.</p>}
    </Part>
  )
}

function LabelsPart({ a, canWrite, busy, pending, subject, run, objectType, objectId }: PartProps) {
  const labels = useAllDefinitions('label', 'false', canWrite)
  const [pick, setPick] = useState('')
  const free = labels.items.filter((l) => !a.labels.some((x) => x.label.id === l.id))
  return (
    <Part title="Labels">
      {a.labels.length === 0 ? (
        <p>No labels.</p>
      ) : (
        <ul className="space-y-1" aria-label="Labels">
          {a.labels.map((l) => (
            <li key={l.label.id} className="flex flex-wrap items-center justify-between gap-2">
              <span>
                <strong>{l.label.name}</strong>
                {l.label.archived && ARCHIVED}
                <br />
                <Origin a={l} />
              </span>
              {canWrite && (
                <Button variant="outline" size="sm" disabled={busy} aria-label={`Remove label ${l.label.name} from ${subject}`} onClick={() => run(`Saved: ${l.label.name} was removed from ${subject}; automatic runs will not add it back.`, () => removeObjectLabel(objectType, objectId, l.label.id), 'remove-label')}>
                  Remove
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {a.removed_labels.length > 0 && (
        <div>
          <p className="text-xs text-muted-foreground">You removed these; automatic runs do not add them back.</p>
          <ul className="space-y-1" aria-label="Removed labels">
            {a.removed_labels.map((l) => (
              <li key={l.label.id} className="flex flex-wrap items-center justify-between gap-2">
                <span>{l.label.name}</span>
                {canWrite && !l.label.archived && (
                  <Button variant="outline" size="sm" disabled={busy} aria-label={`Put label ${l.label.name} back on ${subject}`} onClick={() => run(`Saved: ${l.label.name} is on ${subject} again.`, () => addObjectLabel(objectType, objectId, l.label.id), 'put-back')}>
                    Put back
                  </Button>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
      {canWrite && (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={`Label to add to ${subject}`} name="label" className="min-w-48 grow sm:max-w-xs">
            {(p) => (
              <Select {...p} value={pick} disabled={busy} onChange={(e) => setPick(e.target.value)}>
                <option value="">Choose…</option>
                {free.map((l) => (
                  <option key={l.id} value={l.id}>
                    {l.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Button size="sm" disabled={busy || pick === ''} onClick={() => {
            const l = free.find((x) => x.id === pick)
            run(`Saved: ${l?.name ?? 'the label'} is on ${subject}.`, () => addObjectLabel(objectType, objectId, pick), 'label')
            setPick('')
          }}>
            {pending === 'label' ? 'Saving…' : 'Add label'}
          </Button>
        </div>
      )}
    </Part>
  )
}

function BucketsPart({ a, canWrite, busy, pending, subject, run, objectType, objectId }: PartProps) {
  const buckets = useAllDefinitions('bucket', 'false', canWrite)
  const [pick, setPick] = useState('')
  const type = objectType as 'commitment' | 'account'
  const free = buckets.items.filter((b) => !a.buckets.some((x) => x.id === b.id))
  return (
    <Part title="Buckets">
      {a.buckets.length === 0 ? (
        <p>In no bucket.</p>
      ) : (
        <ul className="space-y-1" aria-label="Buckets">
          {a.buckets.map((b) => (
            <li key={b.id} className="flex flex-wrap items-center justify-between gap-2">
              <span>
                {b.name}
                {b.archived && ARCHIVED}
              </span>
              {canWrite && (
                <Button variant="outline" size="sm" disabled={busy} aria-label={`Take ${subject} out of bucket ${b.name}`} onClick={() => run(`Saved: ${subject} is out of ${b.name}.`, () => removeBucketMember(b.id, type, objectId), 'remove-bucket')}>
                  Remove
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canWrite && (
        <div className="flex flex-wrap items-end gap-2">
          <Field label={`Bucket to put ${subject} in`} name="bucket" hint="A bucket only groups; it holds no money." className="min-w-48 grow sm:max-w-xs">
            {(p) => (
              <Select {...p} value={pick} disabled={busy} onChange={(e) => setPick(e.target.value)}>
                <option value="">Choose…</option>
                {free.map((b) => (
                  <option key={b.id} value={b.id}>
                    {b.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Button size="sm" disabled={busy || pick === ''} onClick={() => {
            const b = free.find((x) => x.id === pick)
            run(`Saved: ${subject} is in ${b?.name ?? 'the bucket'}.`, () => addBucketMember(pick, type, objectId), 'bucket')
            setPick('')
          }}>
            {pending === 'bucket' ? 'Saving…' : 'Add to bucket'}
          </Button>
        </div>
      )}
    </Part>
  )
}
