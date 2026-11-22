import { useEffect, useId, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Store } from 'lucide-react'
import { ApiError, splitErrors } from '@/lib/api'
import { holds, useMe } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Input, Select } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { DateText } from '@/components/formatted'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { QueryState } from '@/components/query-state'
import { useLoadAll } from '@/features/organisation/api'
import { OrganiseDisclosure } from '@/features/organisation/organise-panel'
import { addAlias, archiveMerchant, deleteMerchant, mergeMerchants, merchantKey, merchantsKey, removeAlias, renameMerchant, useMerchant, useMerchants } from './api'
import { Badge } from './merchants-page'
import { isConflict, merchantProblem } from './problems'
import type { MergeResult, MerchantRecord } from './types'

type Notice = { text: string; undo?: () => void }

const ORIGIN = { user: 'added by you', decision: 'from a decision of yours', upgrade: 'from an earlier version' } as const

/** /merchants/$id: one merchant record: its name, aliases, archive state, and merging another merchant into it. */
export function MerchantDetailPage({ id }: { id: string }) {
  const me = useMe()
  const q = useMerchant(id)
  const canWrite = holds(me.data?.grants, 'merchants:write')
  if (me.data && !holds(me.data.grants, 'merchants:read')) {
    return <p role="status" className="text-sm text-muted-foreground">Merchants are shown to administrators only. Your token or role does not hold merchants:read.</p>
  }
  if (q.isError && q.error instanceof ApiError && q.error.status === 404) {
    return (
      <section className="space-y-3" aria-labelledby="merchant-title">
        <h1 id="merchant-title" className="text-2xl font-semibold">Merchant not found</h1>
        <p className="text-sm text-muted-foreground">No merchant has this identifier. It may have been deleted.</p>
        <Link to="/merchants" className="underline underline-offset-4">Back to the merchants</Link>
      </section>
    )
  }
  return (
    <section className="space-y-6" aria-labelledby="merchant-title">
      <p className="text-sm"><Link to="/merchants" className="underline underline-offset-4">All merchants</Link></p>
      <QueryState query={q} label="the merchant">{(m) => <Detail m={m} canWrite={canWrite} />}</QueryState>
    </section>
  )
}

function Detail({ m, canWrite }: { m: MerchantRecord; canWrite: boolean }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const deleted = useRef(false)
  const [renaming, setRenaming] = useState(false)
  const [merging, setMerging] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [rowError, setRowError] = useState<string>()
  const noticeBox = useRef<HTMLDivElement>(null)
  const pendingBox = useRef<HTMLParagraphElement>(null)
  const undoing = useRef(false)
  const merged = m.merged_into !== null
  const active = !m.archived

  // A change that removed the focused button moves focus to the notice, which holds Undo.
  useEffect(() => {
    if (notice) noticeBox.current?.focus()
  }, [notice])
  // Undo removes the focused button while the server answers: focus the "Saving…" status so the wait is announced and focus is not on BODY.
  // A refusal then lands on the error, an acceptance on the new notice.
  // A dialog that produced a notice hands focus to it rather than back to the button that opened the dialog.
  const toNotice = () => {
    if (!noticeBox.current) return false
    noticeBox.current.focus()
    return true
  }

  // Every accepted change replaces the cached record (the server's answer, with its new version) and reloads the lists.
  const accepted = (r: MerchantRecord) => {
    qc.setQueryData(merchantKey(r.id), r)
    return qc.invalidateQueries({ queryKey: merchantsKey })
  }
  const failed = (e: unknown) => {
    setNotice(null)
    setRowError(merchantProblem(e))
    if (isConflict(e)) void qc.invalidateQueries({ queryKey: merchantKey(m.id) })
  }
  const archive = useMutation({
    mutationFn: (v: { to: boolean }) => archiveMerchant(m, v.to),
    onMutate: () => setRowError(undefined),
    onSuccess: async (r, v) => {
      await accepted(r)
      setNotice({
        text: v.to ? `${r.display_name} is archived. Everything that refers to it is kept; nothing new can be attached to it.` : `${r.display_name} is active again.`,
        undo: () => archive.mutate({ to: !v.to }),
      })
    },
    onError: failed,
  })
  const alias = useMutation({
    mutationFn: (v: { add: string } | { remove: string; name: string }) => ('add' in v ? addAlias(m, v.add) : removeAlias(m, v.remove)),
    onMutate: () => setRowError(undefined),
    onSuccess: async (r, v) => {
      await accepted(r)
      setNotice('remove' in v ? { text: `The alias ${v.name} was removed. Transactions with that name no longer resolve to ${r.display_name}. Undo adds it back as an alias you added, not with its earlier origin or date.`, undo: () => alias.mutate({ add: v.name }) } : { text: `The alias ${v.add} was added.` })
    },
    // A failed add is shown beside its field; a failed removal at the top.
    onError: (e, v) => {
      if ('remove' in v) failed(e)
    },
  })
  const pending = archive.isPending || alias.isPending
  useEffect(() => {
    if (pending && undoing.current) pendingBox.current?.focus()
    if (!pending) undoing.current = false
  }, [pending])

  return (
    <div className="space-y-6">
      <header className="space-y-2">
        <h1 id="merchant-title" className="flex items-start gap-2 text-2xl font-semibold break-words">
          <Store aria-hidden className="mt-1 size-6 shrink-0 text-muted-foreground" />
          <span>
            {m.display_name}
            {merged ? <Badge>Merged</Badge> : m.archived ? <Badge>Archived</Badge> : null}
          </span>
        </h1>
        <p className="text-xs text-muted-foreground">
          Identifier <Mono>{m.id}</Mono> · created <DateText value={m.created_at} /> · changed <DateText value={m.updated_at} /> · version {m.version}
        </p>
        {merged && (
          <p role="status" className="text-sm">
            This merchant was merged into <Link to="/merchants/$id" params={{ id: m.merged_into! }} className="underline underline-offset-4">another merchant</Link>. Its aliases, decisions and assignments moved there; this record stays so old references resolve. It cannot be changed.
          </p>
        )}
        {m.archived && !merged && <p className="text-sm text-muted-foreground">Archived: nothing new can be attached to it by identifier, but its aliases still resolve names to it.</p>}
      </header>

      <FormError message={rowError} />
      {notice && (
        <div ref={noticeBox} tabIndex={-1} role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted p-3 text-sm">
          {notice.text}
          {notice.undo && (
            <Button
              variant="outline"
              size="sm"
              disabled={pending}
              onClick={() => {
                const u = notice.undo!
                undoing.current = true
                setNotice(null)
                u()
              }}
            >
              Undo
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => setNotice(null)}>Dismiss</Button>
        </div>
      )}
      {pending && <p ref={pendingBox} tabIndex={-1} role="status" className="text-xs focus:outline-none text-muted-foreground">Saving… this change is not saved until the server confirms it.</p>}

      {canWrite && !merged && (
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" onClick={() => setRenaming(true)} disabled={pending}>Rename</Button>
          <Button variant="outline" disabled={pending} onClick={() => archive.mutate({ to: !m.archived })}>
            {archive.isPending ? 'Saving…' : m.archived ? 'Unarchive' : 'Archive'}
          </Button>
          <Button variant="outline" disabled={pending || !active} aria-describedby={active ? undefined : 'merge-why'} onClick={() => setMerging(true)}>Merge another merchant into this one</Button>
          <Button variant="outline" disabled={pending} onClick={() => setDeleting(true)}>Delete</Button>
        </div>
      )}
      {canWrite && !merged && !active && <p id="merge-why" className="text-xs text-muted-foreground">Unarchive this merchant to merge another into it: only an active merchant can remain.</p>}

      {!merged && (
        <section aria-labelledby="merchant-organise" className="space-y-2">
          <h2 id="merchant-organise" className="text-xl font-semibold">Category and labels</h2>
          <p className="text-sm text-muted-foreground">Assigned to this merchant record, so they survive a rename or a new alias. A category or label you choose here is your own word; automatic assignment never changes it.</p>
          <OrganiseDisclosure objectType="merchant" objectId={m.id} subject={m.display_name} locked={m.archived ? 'This merchant is archived: assignments can be read but not changed. Unarchive it to change them.' : undefined} />
        </section>
      )}
      <Aliases m={m} canWrite={canWrite && !merged} pending={pending} onAdd={(name) => alias.mutateAsync({ add: name })} onRemove={(a) => alias.mutate({ remove: a.id, name: a.name })} />

      <Dialog open={renaming} onOpenChange={setRenaming}>
        <DialogContent focusInstead={toNotice} title={`Rename ${m.display_name}`} description="The new name is also added as an alias. A name another merchant already has is refused: merge the two instead.">
          {renaming && (
            <RenameForm
              m={m}
              onCancel={() => setRenaming(false)}
              onConflict={() => void qc.invalidateQueries({ queryKey: merchantKey(m.id) })}
              onDone={async (r) => {
                await accepted(r)
                setNotice({ text: `Renamed to ${r.display_name}.` })
                setRenaming(false)
              }}
            />
          )}
        </DialogContent>
      </Dialog>
      <Dialog open={merging} onOpenChange={setMerging}>
        <DialogContent focusInstead={toNotice} title={`Merge another merchant into ${m.display_name}`} description="Choose the merchant to absorb. Nothing changes until you confirm the merge.">
          {merging && (
            <MergeForm
              into={m}
              onCancel={() => setMerging(false)}
              onConflict={() => void qc.invalidateQueries({ queryKey: merchantKey(m.id) })}
              onDone={async (r, fromName) => {
                await accepted(r.merchant)
                const mv = r.moved
                setNotice({
                  text: `${fromName} was merged into ${r.merchant.display_name} and archived. Moved: ${mv.aliases} alias${mv.aliases === 1 ? '' : 'es'}, ${mv.decisions} decision${mv.decisions === 1 ? '' : 's'}, ${mv.enrichments} source answer${mv.enrichments === 1 ? '' : 's'}, ${mv.categories} category and ${mv.labels} label assignment${mv.labels === 1 ? '' : 's'}. Left where they were because ${r.merchant.display_name} already had one: ${r.kept}. This cannot be undone in place.`,
                })
                setMerging(false)
              }}
            />
          )}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${m.display_name}`}
        description="Deleting cannot be undone. It is refused while a decision, an assignment or a merged merchant still refers to this merchant: archive it to keep the history, or merge it into another."
        confirmLabel={`Delete ${m.display_name}`}
        // Once deleted, the list heading takes focus when it mounts: the closing dialog must not pull it back to the main region.
        onCloseAutoFocus={(e) => {
          if (deleted.current) e.preventDefault()
        }}
        onConfirm={async () => {
          try {
            await deleteMerchant(m)
          } catch (e) {
            if (isConflict(e)) void qc.invalidateQueries({ queryKey: merchantKey(m.id) })
            throw new Error(merchantProblem(e), { cause: e })
          }
          qc.removeQueries({ queryKey: merchantKey(m.id) })
          await qc.invalidateQueries({ queryKey: merchantsKey })
          deleted.current = true
          // The merchant's page is gone, so focus would fall to BODY: the list's heading takes it when it mounts (one-shot router state).
          await navigate({ to: '/merchants', state: { focusTitle: true } as never })
        }}
      />
    </div>
  )
}

function Aliases({ m, canWrite, pending, onAdd, onRemove }: { m: MerchantRecord; canWrite: boolean; pending: boolean; onAdd: (name: string) => Promise<unknown>; onRemove: (a: MerchantRecord['aliases'][number]) => void }) {
  const [name, setName] = useState('')
  const [local, setLocal] = useState<string>()
  const [err, setErr] = useState<string>()
  const [adding, setAdding] = useState(false)
  const headId = useId()
  return (
    <section aria-labelledby={headId} className="space-y-3">
      <h2 id={headId} className="text-xl font-semibold">Aliases</h2>
      <p className="text-sm text-muted-foreground">A transaction whose merchant name matches an alias (ignoring case, spacing and accents) resolves to this merchant. An alias belongs to one merchant at most.</p>
      {m.aliases.length === 0 ? (
        <p className="text-sm text-muted-foreground">This merchant has no aliases, so no transaction name resolves to it.</p>
      ) : (
        <ul className="space-y-2" aria-label="aliases">
          {m.aliases.map((a) => (
            <li key={a.id} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border bg-surface p-3 text-sm">
              <span className="min-w-0 break-words">
                <span className="font-medium">{a.name}</span> <span className="text-xs text-muted-foreground">{ORIGIN[a.origin]}, <DateText value={a.created_at} /></span>
              </span>
              {canWrite && (
                <Button variant="outline" size="sm" disabled={pending} aria-label={`Remove the alias ${a.name}`} onClick={() => onRemove(a)}>
                  Remove
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
      {canWrite && (
        <form
          className="flex flex-wrap items-start gap-2"
          noValidate
          onSubmit={(e) => {
            e.preventDefault()
            setErr(undefined)
            if (!name.trim()) return setLocal('Enter the alias to add.')
            setLocal(undefined)
            setAdding(true)
            onAdd(name.trim()).then(
              () => setName(''),
              (e2) => setErr(merchantProblem(e2)),
            ).finally(() => setAdding(false))
          }}
        >
          <Field label="Add an alias" name="alias" className="min-w-48 grow sm:max-w-sm" error={local}>
            {(p) => <Input {...p} maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
          </Field>
          <Button type="submit" className="mt-6" disabled={pending || adding}>{adding ? 'Saving…' : 'Add alias'}</Button>
          <div className="basis-full"><FormError message={err} /></div>
        </form>
      )}
    </section>
  )
}

function RenameForm({ m, onDone, onCancel, onConflict }: { m: MerchantRecord; onDone: (r: MerchantRecord) => void | Promise<void>; onCancel: () => void; onConflict: () => void }) {
  const [name, setName] = useState(m.display_name)
  const [local, setLocal] = useState<string>()
  const mut = useMutation({
    mutationFn: () => renameMerchant(m, name.trim()),
    onSuccess: onDone,
    onError: (e) => {
      if (isConflict(e)) onConflict()
    },
  })
  const { fields } = splitErrors(mut.error, ['display_name'])
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l = name.trim() ? undefined : 'Enter a display name.'
        setLocal(l)
        if (!l) mut.mutate()
      }}
    >
      <FormError message={mut.error && !fields.display_name ? merchantProblem(mut.error) : undefined} />
      <Field label="Display name" name="display_name" hint="At most 200 characters." error={local ?? fields.display_name}>
        {(p) => <Input {...p} autoFocus maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={mut.isPending}>Cancel</Button>
        <Button type="submit" disabled={mut.isPending}>{mut.isPending ? 'Saving…' : 'Save name'}</Button>
      </div>
    </form>
  )
}

/** Choose the merchant to absorb, read what moves, acknowledge that it cannot be undone in place, then merge. */
function MergeForm({ into, onDone, onCancel, onConflict }: { into: MerchantRecord; onDone: (r: MergeResult, fromName: string) => void | Promise<void>; onCancel: () => void; onConflict: () => void }) {
  const [fromId, setFromId] = useState('')
  const [ack, setAck] = useState(false)
  const list = useMerchants(false)
  useLoadAll(list)
  const candidates = (list.data ?? []).filter((c) => c.id !== into.id)
  const from = candidates.find((c) => c.id === fromId)
  const mut = useMutation({
    mutationFn: () => mergeMerchants(into, fromId),
    onSuccess: (r) => onDone(r, from?.display_name ?? 'The merchant'),
    onError: (e) => {
      if (isConflict(e)) onConflict()
    },
  })
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        if (from && ack) mut.mutate()
      }}
    >
      <FormError message={mut.error ? merchantProblem(mut.error) : undefined} />
      {list.isError && !list.data && <FormError message={`Could not load the merchants: ${list.error.message}`} />}
      <Field label="Merchant to merge into this one" name="merge_from" hint="Only active merchants are listed. Merging an archived merchant is not offered here.">
        {(p) => (
          <Select
            {...p}
            value={fromId}
            disabled={mut.isPending}
            onChange={(e) => {
              setFromId(e.target.value)
              setAck(false)
            }}
          >
            <option value="">{list.isPending ? 'Loading…' : 'Choose a merchant'}</option>
            {candidates.map((c) => (
              <option key={c.id} value={c.id}>{c.display_name}</option>
            ))}
          </Select>
        )}
      </Field>
      {from && (
        <div className="space-y-3 rounded-md border border-border bg-muted p-3 text-sm" aria-label="What the merge does">
          <p className="font-medium">
            <span>{from.display_name}</span> will be merged into <span>{into.display_name}</span>, which remains.
          </p>
          <ul className="list-disc space-y-1 pl-5">
            <li>
              Its {from.aliases.length} alias{from.aliases.length === 1 ? '' : 'es'} move to {into.display_name}: {from.aliases.length ? from.aliases.map((a) => a.name).join(', ') : 'none'}.
            </li>
            <li>Your confirmations and corrections that name it, the source answers that resolve to it, and the categories and labels you assigned to it move too. The server counts them and tells you when it is done.</li>
            <li>If both have an assignment for the same category or label, {into.display_name}&apos;s stands, unless the other one is yours and {into.display_name}&apos;s is automatic. Anything left behind is counted.</li>
            <li>{from.display_name} is archived and points to {into.display_name}. It keeps its identifier so older references still resolve.</li>
          </ul>
          <p className="font-medium" role="note">
            A merge cannot be undone in place. The audit log keeps what moved; separating them again means creating a merchant and re-assigning by hand.
          </p>
          <label className="flex items-start gap-2">
            <input type="checkbox" className="mt-1" checked={ack} disabled={mut.isPending} onChange={(e) => setAck(e.target.checked)} />
            <span>I understand this merge cannot be undone in place.</span>
          </label>
        </div>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={mut.isPending}>Cancel</Button>
        <Button type="submit" disabled={mut.isPending || !from || !ack}>{mut.isPending ? 'Merging…' : 'Merge'}</Button>
      </div>
    </form>
  )
}
