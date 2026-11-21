import { useId, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { apiFetch, splitErrors } from '@/lib/api'
import { api, holds, useMe } from '@/lib/auth'
import type { CreatedApiToken, GrantVocabulary } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { Notice } from '@/components/notice'

export const ROOT_WARNING =
  'A root token holds every grant your role holds, now and in later versions of Ledger, and never expires unless you set an expiry. Anyone with it can act as you through the API. Store it like a password.'

/**
 * Creates a token. The grants picker offers only grants the signed-in user holds (the server refuses
 * the rest: no escalation). Nothing is shown as created until the server answers; the secret is
 * handed to onCreated and never kept here.
 */
export function CreateTokenDialog({
  open,
  onOpenChange,
  onCreated,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onCreated: (c: CreatedApiToken) => void
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        title="Create an API token"
        description="The token is shown once, right after it is created. Choose only the grants it needs."
      >
        <CreateForm onCancel={() => onOpenChange(false)} onCreated={onCreated} />
      </DialogContent>
    </Dialog>
  )
}

function CreateForm({ onCancel, onCreated }: { onCancel: () => void; onCreated: (c: CreatedApiToken) => void }) {
  const me = useMe()
  const vocab = useQuery({ queryKey: ['grants'], queryFn: () => apiFetch<GrantVocabulary>(api('/grants')) })
  if (me.isPending || vocab.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Loading your grants…
      </p>
    )
  }
  if (!me.data || !vocab.data) {
    return (
      <div className="space-y-3">
        <FormError message="Could not load your grants, so no token can be created now." />
        <Button variant="outline" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }
  return <Inner vocab={vocab.data} mine={me.data.grants} onCancel={onCancel} onCreated={onCreated} />
}

function Inner({
  vocab,
  mine,
  onCancel,
  onCreated,
}: {
  vocab: GrantVocabulary
  mine: string[]
  onCancel: () => void
  onCreated: (c: CreatedApiToken) => void
}) {
  const all = vocab.resources.flatMap((r) => r.actions.map((a) => `${r.name}:${a}`))
  const offered = vocab.resources
    .map((r) => ({ ...r, actions: r.actions.filter((a) => holds(mine, `${r.name}:${a}`)) }))
    .filter((r) => r.actions.length > 0)
  const mayRoot = all.length > 0 && all.every((g) => holds(mine, g))
  const [name, setName] = useState('')
  const [picked, setPicked] = useState<Set<string>>(new Set())
  const [root, setRoot] = useState(false)
  const [expires, setExpires] = useState('')
  const [local, setLocal] = useState<Record<string, string>>({})
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const uid = useId()
  const { fields, form } = splitErrors(error, ['name', 'grants', 'root', 'expires_at'])
  const err = { ...fields, ...local }

  async function submit() {
    const l: Record<string, string> = {}
    if (!name.trim()) l.name = 'Give the token a name.'
    if (!root && picked.size === 0) l.grants = 'Choose at least one grant, or make it a root token.'
    if (expires && new Date(expires).getTime() <= Date.now()) l.expires_at = 'The expiry must be in the future.'
    setLocal(l)
    if (Object.keys(l).length) return
    setPending(true)
    setError(undefined)
    try {
      // Plain call, not a mutation: a mutation result would sit in the query client's cache with the secret.
      const created = await apiFetch<CreatedApiToken>(api('/auth/tokens'), {
        method: 'POST',
        json: {
          name: name.trim(),
          ...(root ? { root: true } : { grants: [...picked].sort() }),
          ...(expires ? { expires_at: new Date(expires).toISOString() } : {}),
        },
      })
      onCreated(created)
    } catch (e) {
      setError(e)
      setPending(false)
    }
  }

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        void submit()
      }}
    >
      <FormError message={form} />
      <Field label="Name" name="name" hint="So you can recognise it later, for example the tool that uses it." error={err.name}>
        {(p) => <Input {...p} maxLength={100} autoFocus autoComplete="off" value={name} onChange={(e) => setName(e.target.value)} />}
      </Field>

      <fieldset className="space-y-2" disabled={root} aria-describedby={err.grants ? `${uid}-grants-error` : undefined}>
        <legend className="text-sm font-medium">Grants</legend>
        <p className="text-xs text-muted-foreground">Only grants your own role holds are offered. A write grant also allows reading.</p>
        {offered.map((r) => (
          <div key={r.name} className="rounded-md border border-border p-2">
            <p className="text-sm font-medium">{r.name}</p>
            <p className="text-xs text-muted-foreground">{r.description}</p>
            <div className="mt-1 flex flex-wrap gap-x-4 gap-y-1">
              {r.actions.map((a) => {
                const g = `${r.name}:${a}`
                return (
                  <label key={g} className="flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      className="size-4 accent-primary"
                      checked={picked.has(g)}
                      aria-invalid={err.grants ? true : undefined}
                      aria-describedby={err.grants ? `${uid}-grants-error` : undefined}
                      onChange={(e) => {
                        const n = new Set(picked)
                        if (e.target.checked) n.add(g)
                        else n.delete(g)
                        setPicked(n)
                      }}
                    />
                    <span>
                      <Mono>{g}</Mono>
                    </span>
                  </label>
                )
              })}
            </div>
          </div>
        ))}
        {err.grants && (
          <p id={`${uid}-grants-error`} className="text-xs font-medium text-status-error">
            {err.grants}
          </p>
        )}
      </fieldset>

      {mayRoot && (
        <div className="space-y-2">
          <label className="flex items-center gap-2 text-sm font-medium">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={root}
              onChange={(e) => setRoot(e.target.checked)}
              aria-describedby={[`${uid}-root`, err.root ? `${uid}-root-error` : ''].filter(Boolean).join(' ')}
              aria-invalid={err.root ? true : undefined}
            />
            Root token
          </label>
          <Notice id={`${uid}-root`} role={root ? 'alert' : 'note'} className="text-xs">
            {ROOT_WARNING}
          </Notice>
          {err.root && <p id={`${uid}-root-error`} className="text-xs font-medium text-status-error">{err.root}</p>}
        </div>
      )}

      <Field label="Expires (optional)" name="expires_at" hint="Local time. Leave empty for a token that never expires." error={err.expires_at}>
        {(p) => <Input {...p} type="datetime-local" value={expires} onChange={(e) => setExpires(e.target.value)} />}
      </Field>

      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={pending}>
          Cancel
        </Button>
        <Button type="submit" disabled={pending}>
          {pending ? 'Creating…' : 'Create token'}
        </Button>
      </div>
    </form>
  )
}
