import { useDeferredValue, useEffect, useRef, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, NetworkError, splitErrors } from '@/lib/api'
import { holds, useMe } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Input, Select } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { QueryState } from '@/components/query-state'
import { EmptyState } from '@/components/state-panels'
import { useCommitmentName } from '@/features/matches/api'
import {
  archiveDefinition,
  createDefinition,
  deleteDefinition,
  orgKey,
  patchDefinition,
  removeBucketMember,
  useAllDefinitions,
  useBucketMembers,
  useDefinitions,
  useLoadAll,
} from './api'
import { KIND_NAME, KINDS, type BucketMember, type Definition, type Kind } from './types'

/** Why a change was not saved, in words: nothing is shown as saved until the server accepted it. */
export function problemText(e: unknown): string {
  if (e instanceof NetworkError) return 'Not saved: there is no connection to the server. Nothing was changed.'
  if (e instanceof ApiError && e.code === 'merchant_archived') return 'This merchant is archived or merged, so nothing new can be put on it. Unarchive it first (a merged merchant stays merged). Nothing was changed.'
  if (e instanceof ApiError && e.code === 'version_conflict') return 'This was changed elsewhere. The list is reloaded: check it and try again. Nothing was changed.'
  return e instanceof Error ? e.message : 'Something went wrong. Nothing was changed.'
}

type Notice = { text: string; undo?: () => void }

/** /organisation: the categories (two levels), labels and buckets a person organises with. */
export function OrganisationPage() {
  const me = useMe()
  const canWrite = holds(me.data?.grants, 'organisation:write')
  return (
    <section className="space-y-6" aria-labelledby="organisation-title">
      <header className="space-y-2">
        <h1 id="organisation-title" className="text-2xl font-semibold">
          Categories, labels and buckets
        </h1>
        <p className="text-sm text-muted-foreground">
          Categories (one per item, with at most two levels) and labels (any number) describe transactions, commitments and bank accounts. Buckets group commitments and bank accounts; a bucket holds no money and allocates none.
          Assign them where the item is shown: open <strong>Organise</strong> on an occurrence, a bank account or a suggested payment.
        </p>
        <p className="text-sm text-muted-foreground">
          Archiving keeps a definition and everything assigned to it, and only stops new use. Deleting is for one that nothing uses, or that you move to another first. Merchants are organised on their own page ({'/merchants/<id>'}), and that needs the administrator-only merchants:read and merchants:write grants as well.
        </p>
        {!canWrite && <p className="text-sm text-muted-foreground">You can read these but not change them: your token or role does not hold organisation:write.</p>}
      </header>
      {KINDS.map((k) => (
        <DefinitionSection key={k} kind={k} canWrite={canWrite} />
      ))}
    </section>
  )
}

function DefinitionSection({ kind, canWrite }: { kind: Kind; canWrite: boolean }) {
  const qc = useQueryClient()
  const names = KIND_NAME[kind]
  const [showArchived, setShowArchived] = useState(false)
  const [prefix, setPrefix] = useState('')
  const deferred = useDeferredValue(prefix.trim())
  const list = useDefinitions(kind, { archived: showArchived ? undefined : 'false', prefix: deferred || undefined })
  useLoadAll(list)
  const [editing, setEditing] = useState<Definition | 'new' | null>(null)
  const [deleting, setDeleting] = useState<Definition | null>(null)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [rowError, setRowError] = useState<string>()
  const noticeBox = useRef<HTMLDivElement>(null)
  const heading = useRef<HTMLHeadingElement>(null)
  // Set by a successful delete: only then must the closing dialog not hand focus back (its trigger is gone).
  const deletedRef = useRef(false)
  // A row that was archived or deleted takes the focused button with it: focus follows the change to its notice (which holds Undo).
  // After a successful delete the dialog does not hand focus back (onCloseAutoFocus below), so nothing competes with the notice.
  useEffect(() => {
    if (notice) noticeBox.current?.focus()
  }, [notice])
  // Children for the parent hint come from the whole list, not the filtered one: archived children and a name filter must not hide them.
  const everyCategory = useAllDefinitions('category', undefined, kind === 'category')
  const refresh = () => qc.invalidateQueries({ queryKey: orgKey })

  // Archiving is reversible, so it needs no confirmation: it says what happened and offers Undo. The notice appears only
  // after the server has accepted the change.
  const archive = useMutation({
    mutationFn: (v: { d: Definition; archive: boolean }) => archiveDefinition(kind, v.d, v.archive),
    onMutate: () => setRowError(undefined),
    onSuccess: async (d, v) => {
      await refresh()
      setNotice({
        text: v.archive ? `${d.name} is archived. Everything assigned to it is kept and says archived; nothing new can use it.` : `${d.name} is active again.`,
        undo: () => archive.mutate({ d, archive: !v.archive }),
      })
    },
    onError: (e) => {
      setNotice(null)
      setRowError(problemText(e))
      void refresh()
    },
  })

  const items = list.data ?? []
  const childrenOf = (id: string) => items.filter((i) => i.parent_id === id)
  const tree = kind === 'category' && !deferred
  const roots = tree ? items.filter((i) => i.parent_id === null || !items.some((p) => p.id === i.parent_id)) : items
  const nameOf = (id: string | null) => items.find((i) => i.id === id)?.name

  const row = (d: Definition, depth: number) => (
    <li key={d.id} className="space-y-2">
      <div className={`flex flex-wrap items-center justify-between gap-3 rounded-md border border-border bg-surface p-3 ${depth ? 'ml-6' : ''}`}>
        <div className="min-w-0 text-sm">
          <p className="font-medium break-words">
            {d.name}
            {d.archived && <span className="ml-2 rounded border border-border px-1.5 py-0.5 text-xs font-normal text-muted-foreground">Archived</span>}
          </p>
          {kind === 'category' && d.parent_id && !tree && <p className="text-xs text-muted-foreground">Under {nameOf(d.parent_id) ?? 'another category'}</p>}
        </div>
        <div className="flex flex-wrap gap-2">
          {kind === 'bucket' && <MembersToggle bucket={d} canWrite={canWrite} />}
          {canWrite && (
            <>
              <Button variant="outline" size="sm" aria-label={`Rename ${d.name}`} onClick={() => setEditing(d)}>
                {kind === 'category' ? 'Rename or move' : 'Rename'}
              </Button>
              <Button variant="outline" size="sm" disabled={archive.isPending} aria-label={`${d.archived ? 'Unarchive' : 'Archive'} ${d.name}`} onClick={() => archive.mutate({ d, archive: !d.archived })}>
                {archive.isPending && archive.variables?.d.id === d.id ? 'Saving…' : d.archived ? 'Unarchive' : 'Archive'}
              </Button>
              <Button variant="outline" size="sm" aria-label={`Delete ${d.name}`} onClick={() => setDeleting(d)}>
                Delete
              </Button>
            </>
          )}
        </div>
      </div>
      {tree && childrenOf(d.id).length > 0 && <ul className="space-y-2">{childrenOf(d.id).map((c) => row(c, depth + 1))}</ul>}
    </li>
  )

  return (
    <section aria-labelledby={`org-${kind}`} className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id={`org-${kind}`} ref={heading} tabIndex={-1} className="text-xl font-semibold capitalize outline-none">
          {names.many}
        </h2>
        {canWrite && <Button onClick={() => setEditing('new')}>Add {names.one}</Button>}
      </div>
      <div className="flex flex-wrap items-end gap-3">
        <Field label={`Find a ${names.one} by name`} name={`${kind}-find`} className="min-w-48 grow sm:max-w-xs">
          {(p) => <Input {...p} type="search" value={prefix} onChange={(e) => setPrefix(e.target.value)} />}
        </Field>
        <label className="flex items-center gap-2 pb-2 text-sm">
          <input type="checkbox" checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} />
          Show archived {names.many}
        </label>
      </div>
      <FormError message={rowError} />
      {notice && (
        <div ref={noticeBox} tabIndex={-1} role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted p-3 text-sm">
          {notice.text}
          {notice.undo && (
            <Button
              variant="outline"
              size="sm"
              disabled={archive.isPending}
              onClick={() => {
                const u = notice.undo!
                setNotice(null)
                u()
              }}
            >
              Undo
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              setNotice(null)
              heading.current?.focus()
            }}
          >
            Dismiss
          </Button>
        </div>
      )}
      <QueryState query={list} label={names.many}>
        {() =>
          items.length === 0 ? (
            <EmptyState id={`org-${kind}-empty`} level={3} title={deferred ? `No ${names.one} starts with that text` : showArchived ? `No ${names.many} yet` : `No active ${names.many}`}>
              {canWrite ? `Use Add ${names.one} to create one.` : 'Nothing has been created yet.'}
            </EmptyState>
          ) : (
            <>
              <ul className="space-y-2" aria-label={names.many}>
                {roots.map((d) => row(d, 0))}
              </ul>
              {list.hasNextPage && (
                <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  {list.isFetchingNextPage ? 'Loading…' : `Load more ${names.many}`}
                </Button>
              )}
            </>
          )
        }
      </QueryState>

      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent
          title={editing === 'new' || editing === null ? `Add a ${names.one}` : `Rename ${editing.name}`}
          description={kind === 'category' ? 'Names are unique among sibling categories, ignoring case, archived ones included.' : `Names are unique among ${names.many}, ignoring case, archived ones included.`}
        >
          {editing !== null && (
            <DefinitionForm
              kind={kind}
              definition={editing === 'new' ? undefined : editing}
              hasChildren={editing !== 'new' && everyCategory.items.some((c) => c.parent_id === editing.id)}
              onDone={async (d, created) => {
                setEditing(null)
                await refresh()
                setNotice({ text: created ? `${d.name} was added.` : `${d.name} was saved.` })
              }}
              onCancel={() => setEditing(null)}
              onConflict={refresh}
            />
          )}
        </DialogContent>
      </Dialog>
      <Dialog open={deleting !== null} onOpenChange={(o) => !o && setDeleting(null)}>
        <DialogContent
          onCloseAutoFocus={(e) => {
            // Cancel, Escape and overlay clicks keep the shared return-focus to the Delete trigger.
            if (deletedRef.current) {
              e.preventDefault()
              deletedRef.current = false
            }
          }}
          title={`Delete ${deleting?.name ?? names.one}`}
          description={`Deleting cannot be undone. Archive it instead to keep the history. A ${names.one} that is still assigned is only deleted after you move its assignments to another.`}
        >
          {deleting && (
            <DeleteForm
              kind={kind}
              definition={deleting}
              onDone={async (n) => {
                const d = deleting
                deletedRef.current = true
                setDeleting(null)
                await refresh()
                setNotice({ text: `${d.name} was deleted${n > 0 ? `; ${n} assignment${n === 1 ? '' : 's'} moved to the replacement` : ''}.` })
              }}
              onCancel={() => setDeleting(null)}
              onConflict={refresh}
            />
          )}
        </DialogContent>
      </Dialog>
    </section>
  )
}

function DefinitionForm({
  kind,
  definition,
  hasChildren,
  onDone,
  onCancel,
  onConflict,
}: {
  kind: Kind
  definition?: Definition
  hasChildren: boolean
  onDone: (d: Definition, created: boolean) => void | Promise<void>
  onCancel: () => void
  onConflict: () => void
}) {
  const names = KIND_NAME[kind]
  const [name, setName] = useState(definition?.name ?? '')
  const [parent, setParent] = useState(definition?.parent_id ?? '')
  const [local, setLocal] = useState<Record<string, string>>({})
  const parents = useAllDefinitions('category', 'false', kind === 'category')
  const m = useMutation({
    mutationFn: () => {
      if (!definition) return createDefinition(kind, { name: name.trim(), ...(kind === 'category' && parent ? { parent_id: parent } : {}) })
      // Merge patch: only what changed.
      const patch: { name?: string; parent_id?: string | null } = {}
      if (name.trim() !== definition.name) patch.name = name.trim()
      if (kind === 'category' && parent !== (definition.parent_id ?? '')) patch.parent_id = parent || null
      return patchDefinition(kind, definition, patch)
    },
    onSuccess: (d) => onDone(d, !definition),
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'version_conflict') onConflict()
    },
  })
  const { fields, form } = splitErrors(m.error, ['name', 'parent_id'])
  const err = { ...fields, ...local }
  const conflict = m.error instanceof ApiError && m.error.code === 'version_conflict'
  const duplicate = m.error instanceof ApiError && m.error.code === 'duplicate_name'
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l: Record<string, string> = {}
        if (!name.trim()) l.name = `Enter a name for the ${names.one}.`
        setLocal(l)
        if (!l.name) m.mutate()
      }}
    >
      <FormError message={conflict ? problemText(m.error) : duplicate ? undefined : form} />
      <Field label="Name" name="name" hint="Free text, at most 80 characters." error={err.name ?? (duplicate ? (m.error as Error).message : undefined)}>
        {(p) => <Input {...p} autoFocus maxLength={80} value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>
      {kind === 'category' && (
        <Field
          label="Parent category"
          name="parent_id"
          hint={hasChildren ? 'This category has children, so it stays top-level: categories have two levels.' : 'Leave empty for a top-level category. A parent must be an active top-level category.'}
          error={err.parent_id}
        >
          {(p) => (
            <Select {...p} value={parent} disabled={hasChildren} onChange={(e) => setParent(e.target.value)}>
              <option value="">None (top level)</option>
              {parents.items
                .filter((c) => c.parent_id === null && c.id !== definition?.id)
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.name}
                  </option>
                ))}
            </Select>
          )}
        </Field>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={m.isPending}>
          Cancel
        </Button>
        <Button type="submit" disabled={m.isPending}>
          {m.isPending ? 'Saving…' : definition ? 'Save changes' : `Add ${names.one}`}
        </Button>
      </div>
    </form>
  )
}

/** Delete, and when the definition is still assigned (409 definition_in_use) the choice of a replacement to move to. */
function DeleteForm({ kind, definition, onDone, onCancel, onConflict }: { kind: Kind; definition: Definition; onDone: (reassigned: number) => void | Promise<void>; onCancel: () => void; onConflict: () => void }) {
  const names = KIND_NAME[kind]
  const [to, setTo] = useState('')
  const candidates = useAllDefinitions(kind, 'false')
  const m = useMutation({
    mutationFn: () => deleteDefinition(kind, definition, to || undefined),
    onSuccess: (r) => onDone(r.reassigned),
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'version_conflict') onConflict()
    },
  })
  const inUse = m.error instanceof ApiError && m.error.code === 'definition_in_use'
  const others = candidates.items.filter((c) => c.id !== definition.id)
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        m.mutate()
      }}
    >
      {inUse ? (
        <p role="alert" className="rounded-md border-2 border-warning bg-warning-bg p-3 text-sm text-warning">
          {m.error?.message} Archive it to keep the history, or move what is assigned to another {names.one} and delete this one.
        </p>
      ) : (
        <FormError message={m.error ? problemText(m.error) : undefined} />
      )}
      {(inUse || to !== '') && (
        <Field label={`Move the assignments to`} name="reassign_to" hint={`Only an active ${names.one} can take them. The people's own choices stay theirs; automatic ones stay automatic.`}>
          {(p) => (
            <Select {...p} value={to} onChange={(e) => setTo(e.target.value)}>
              <option value="">Choose a {names.one}</option>
              {others.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={m.isPending}>
          Cancel
        </Button>
        <Button type="submit" disabled={m.isPending || (inUse && to === '')}>
          {m.isPending ? 'Deleting…' : inUse ? 'Move assignments and delete' : `Delete ${definition.name}`}
        </Button>
      </div>
    </form>
  )
}

function MembersToggle({ bucket, canWrite }: { bucket: Definition; canWrite: boolean }) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button variant="outline" size="sm" aria-expanded={open} aria-label={`Members of ${bucket.name}`} onClick={() => setOpen(!open)}>
        Members
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent title={`Members of ${bucket.name}`} description="The commitments and bank accounts grouped in this bucket. Add one from its Organise panel; the bucket holds no money.">
          <MembersList bucket={bucket} canWrite={canWrite} />
        </DialogContent>
      </Dialog>
    </>
  )
}

function MemberName({ m }: { m: BucketMember }) {
  const name = useCommitmentName(m.object_type === 'commitment' ? m.object_id : undefined)
  return (
    <span className="min-w-0 break-words">
      {m.object_type === 'commitment' ? `Commitment ${name.data ?? ''}` : 'Bank account'} <Mono>{m.object_id}</Mono>
    </span>
  )
}

function MembersList({ bucket, canWrite }: { bucket: Definition; canWrite: boolean }) {
  const qc = useQueryClient()
  const q = useBucketMembers(bucket.id)
  useLoadAll(q)
  const remove = useMutation({
    mutationFn: (m: BucketMember) => removeBucketMember(bucket.id, m.object_type, m.object_id),
    onSettled: () => qc.invalidateQueries({ queryKey: orgKey }),
  })
  return (
    <div className="space-y-3">
      <FormError message={remove.isError ? problemText(remove.error) : undefined} />
      <QueryState query={q} label="members">
        {(members) =>
          members.length === 0 ? (
            <p className="text-sm text-muted-foreground">Nothing is in this bucket.</p>
          ) : (
            <ul className="space-y-2" aria-label="Members">
              {members.map((m) => (
                <li key={m.object_type + m.object_id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                  <MemberName m={m} />
                  {canWrite && (
                    <Button variant="outline" size="sm" disabled={remove.isPending} aria-label={`Remove ${m.object_type} ${m.object_id} from ${bucket.name}`} onClick={() => remove.mutate(m)}>
                      {remove.isPending && remove.variables?.object_id === m.object_id ? 'Removing…' : 'Remove'}
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )
        }
      </QueryState>
    </div>
  )
}
