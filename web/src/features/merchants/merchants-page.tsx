import { useDeferredValue, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { splitErrors } from '@/lib/api'
import { holds, useMe } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { QueryState } from '@/components/query-state'
import { EmptyState } from '@/components/state-panels'
import { useLoadAll } from '@/features/organisation/api'
import { createMerchant, merchantsKey, useMerchants } from './api'
import { merchantProblem } from './problems'
import type { MerchantRecord } from './types'

export const Badge = ({ children }: { children: string }) => <span className="ml-2 rounded border border-border px-1.5 py-0.5 text-xs font-normal text-muted-foreground">{children}</span>

/** /merchants: the merchant records transactions are matched to. Names derive from bank text, so the whole screen needs merchants:read. */
export function MerchantsPage() {
  const me = useMe()
  const qc = useQueryClient()
  const canWrite = holds(me.data?.grants, 'merchants:write')
  const [showArchived, setShowArchived] = useState(false)
  const [find, setFind] = useState('')
  const deferred = useDeferredValue(find.trim().toLowerCase())
  const [adding, setAdding] = useState(false)
  const [notice, setNotice] = useState<string>()
  const list = useMerchants(showArchived)
  useLoadAll(list)
  const all = list.data ?? []
  // The server filters only by archived: names and aliases are searched here, over every loaded record.
  const items = deferred ? all.filter((m) => m.display_name.toLowerCase().includes(deferred) || m.aliases.some((a) => a.key.includes(deferred))) : all

  if (!holds(me.data?.grants, 'merchants:read') && me.data) {
    return (
      <section className="space-y-3" aria-labelledby="merchants-title">
        <h1 id="merchants-title" className="text-2xl font-semibold">Merchants</h1>
        <p role="status" className="text-sm text-muted-foreground">Merchants are shown to administrators only: their names come from bank text. Your token or role does not hold merchants:read.</p>
      </section>
    )
  }
  return (
    <section className="space-y-6" aria-labelledby="merchants-title">
      <header className="space-y-2">
        <h1 id="merchants-title" className="text-2xl font-semibold">Merchants</h1>
        <p className="text-sm text-muted-foreground">
          A merchant record is a display name and the alias names that transactions are matched by. Your confirmations and corrections, and the categories and labels you assign, belong to the record, so renaming it changes
          nothing else. Nothing is merged automatically: two spellings stay two merchants until you merge them. Merchant identity is presentation only; it never changes an amount or a payment status.
        </p>
        {!canWrite && <p className="text-sm text-muted-foreground">You can read these but not change them: your token or role does not hold merchants:write.</p>}
      </header>
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div className="flex flex-wrap items-end gap-3">
          <Field label="Find a merchant by name or alias" name="merchant-find" className="min-w-48 grow sm:max-w-xs">
            {(p) => <Input {...p} type="search" value={find} onChange={(e) => setFind(e.target.value)} />}
          </Field>
          <label className="flex items-center gap-2 pb-2 text-sm">
            <input type="checkbox" checked={showArchived} onChange={(e) => setShowArchived(e.target.checked)} />
            Show archived and merged merchants
          </label>
        </div>
        {canWrite && <Button onClick={() => setAdding(true)}>Add merchant</Button>}
      </div>
      {notice && (
        <div role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted p-3 text-sm">
          {notice}
          <Button variant="ghost" size="sm" onClick={() => setNotice(undefined)}>Dismiss</Button>
        </div>
      )}
      <QueryState query={list} label="merchants">
        {() =>
          items.length === 0 ? (
            <EmptyState id="merchants-empty" level={2} title={deferred ? 'No merchant matches that text' : showArchived ? 'No merchants yet' : 'No active merchants'}>
              {deferred
                ? 'Names and aliases are searched in the merchants loaded so far.'
                : 'Merchants appear when you confirm or correct the merchant of a transaction, or add one here.'}
            </EmptyState>
          ) : (
            <>
              <ul className="space-y-2" aria-label="merchants">
                {items.map((m) => (
                  <MerchantRow key={m.id} m={m} />
                ))}
              </ul>
              {list.hasNextPage && (
                <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                  {list.isFetchingNextPage ? 'Loading…' : 'Load more merchants'}
                </Button>
              )}
            </>
          )
        }
      </QueryState>
      <Dialog open={adding} onOpenChange={setAdding}>
        <DialogContent title="Add a merchant" description="The display name becomes the merchant's first alias. Add more aliases on its page.">
          {adding && (
            <CreateForm
              onCancel={() => setAdding(false)}
              onDone={async (m) => {
                setAdding(false)
                await qc.invalidateQueries({ queryKey: merchantsKey })
                setNotice(`${m.display_name} was added.`)
              }}
            />
          )}
        </DialogContent>
      </Dialog>
    </section>
  )
}

function MerchantRow({ m }: { m: MerchantRecord }) {
  const others = m.aliases.filter((a) => a.name !== m.display_name)
  return (
    <li className="rounded-md border border-border bg-surface p-3 text-sm">
      <p className="font-medium break-words">
        <Link to="/merchants/$id" params={{ id: m.id }} className="underline underline-offset-4">
          {m.display_name}
        </Link>
        {m.merged_into ? <Badge>Merged</Badge> : m.archived ? <Badge>Archived</Badge> : null}
      </p>
      <p className="text-xs text-muted-foreground break-words">
        {others.length === 0 ? 'No other aliases.' : `Also matched as: ${others.map((a) => a.name).join(', ')}`}
      </p>
    </li>
  )
}

function CreateForm({ onDone, onCancel }: { onDone: (m: MerchantRecord) => void | Promise<void>; onCancel: () => void }) {
  const [name, setName] = useState('')
  const [local, setLocal] = useState<string>()
  const m = useMutation({ mutationFn: () => createMerchant({ display_name: name.trim() }), onSuccess: onDone })
  const { fields } = splitErrors(m.error, ['display_name'])
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l = name.trim() ? undefined : 'Enter a display name for the merchant.'
        setLocal(l)
        if (!l) m.mutate()
      }}
    >
      <FormError message={m.error && !fields.display_name ? merchantProblem(m.error) : undefined} />
      <Field label="Display name" name="display_name" hint="At most 200 characters. Another merchant may not already have this name as an alias." error={local ?? fields.display_name}>
        {(p) => <Input {...p} autoFocus maxLength={200} value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={m.isPending}>Cancel</Button>
        <Button type="submit" disabled={m.isPending}>{m.isPending ? 'Saving…' : 'Add merchant'}</Button>
      </div>
    </form>
  )
}
