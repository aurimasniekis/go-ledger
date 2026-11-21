import { useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError, apiFetch, splitErrors } from '@/lib/api'
import { api, holds, useMe } from '@/lib/auth'
import { usePagedList } from '@/lib/paged'
import type { Role, User } from '@/lib/types'
import { formatInstant } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Input, Select } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { QueryState } from '@/components/query-state'

const ROLES: Role[] = ['administrator', 'read-only']

type Notice = { text: string; undo?: () => void }

export function UsersPage() {
  const qc = useQueryClient()
  const me = useMe()
  const canWrite = holds(me.data?.grants, 'users:write')
  const list = usePagedList<User>(['users'], api('/users'))
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<User | null>(null)
  const [passwordFor, setPasswordFor] = useState<User | null>(null)
  const [notice, setNotice] = useState<Notice | null>(null)
  const [rowError, setRowError] = useState<string>()
  const refresh = () => qc.invalidateQueries({ queryKey: ['users'] })

  // Disabling is reversible, so it needs no confirmation: it says what happened and offers Undo.
  // The notice appears only after the server has accepted the change.
  const toggle = useMutation({
    mutationFn: (u: User) =>
      apiFetch<User>(api(`/users/${u.id}`), {
        method: 'PATCH',
        headers: { 'If-Match': `"${u.version}"` },
        json: { disabled: !u.disabled },
      }),
    onMutate: () => setRowError(undefined),
    onSuccess: async (u) => {
      await refresh()
      setNotice({
        text: u.disabled
          ? `${u.username} is disabled; their sessions and tokens stopped working.`
          : `${u.username} is enabled again.`,
        undo: () => toggle.mutate(u),
      })
    },
    onError: (e) => {
      setNotice(null)
      setRowError(e instanceof ApiError && e.code === 'version_conflict' ? 'That user was changed elsewhere. The list is refreshed; check it and try again.' : e.message)
      void refresh()
    },
  })

  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold">Users</h1>
        {canWrite && <Button onClick={() => setCreating(true)}>Add user</Button>}
      </div>
      <p className="text-sm text-muted-foreground">
        Users are disabled, never deleted, so the audit history stays intact. Changes here ask for your password again if you
        have not confirmed it recently.
      </p>
      <FormError message={rowError} />
      {notice && (
        <p role="status" className="flex flex-wrap items-center gap-2 rounded-md border border-border bg-muted p-3 text-sm">
          {notice.text}
          {notice.undo && (
            <Button
              variant="outline"
              size="sm"
              disabled={toggle.isPending}
              onClick={() => {
                const u = notice.undo!
                setNotice(null)
                u()
              }}
            >
              Undo
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => setNotice(null)}>
            Dismiss
          </Button>
        </p>
      )}
      <QueryState query={list} label="users">
        {(users) => (
          <>
            <ul className="space-y-2" aria-label="Users">
              {users.map((u) => (
                <li key={u.id} className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border bg-surface p-3">
                  <div className="min-w-0 text-sm">
                    <p className="font-medium break-words">
                      {u.display_name ? `${u.display_name} ` : ''}
                      <Mono>{u.username}</Mono>
                      {u.id === me.data?.user_id && <span className="ml-2 text-xs text-muted-foreground">(you)</span>}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {u.role}
                      {u.disabled ? ` · Disabled ${formatInstant(u.disabled_at)}` : ' · Enabled'}
                    </p>
                  </div>
                  {canWrite && (
                    <div className="flex flex-wrap gap-2">
                      <Button variant="outline" size="sm" aria-label={`Edit ${u.username}`} onClick={() => setEditing(u)}>
                        Edit
                      </Button>
                      <Button variant="outline" size="sm" aria-label={`Set password for ${u.username}`} onClick={() => setPasswordFor(u)}>
                        Set password
                      </Button>
                      <Button
                        variant="outline"
                        size="sm"
                        disabled={toggle.isPending}
                        aria-label={`${u.disabled ? 'Enable' : 'Disable'} ${u.username}`}
                        onClick={() => toggle.mutate(u)}
                      >
                        {toggle.isPending && toggle.variables?.id === u.id ? 'Saving…' : u.disabled ? 'Enable' : 'Disable'}
                      </Button>
                    </div>
                  )}
                </li>
              ))}
            </ul>
            {list.hasNextPage && (
              <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                {list.isFetchingNextPage ? 'Loading…' : 'Load more users'}
              </Button>
            )}
          </>
        )}
      </QueryState>

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent title="Add a user" description="The user is created enabled, without a password. Set one afterwards so they can sign in.">
          <UserForm
            onDone={async (u) => {
              setCreating(false)
              await refresh()
              setNotice({ text: `${u.username} was added. Set a password so they can sign in.` })
            }}
            onCancel={() => setCreating(false)}
          />
        </DialogContent>
      </Dialog>
      <Dialog open={editing !== null} onOpenChange={(o) => !o && setEditing(null)}>
        <DialogContent title={`Edit ${editing?.username ?? 'user'}`} description="Only the fields you change are sent.">
          {editing && (
            <UserForm
              user={editing}
              onDone={async (u) => {
                setEditing(null)
                await refresh()
                setNotice({ text: `${u.username} was saved.` })
              }}
              onCancel={() => setEditing(null)}
              onConflict={refresh}
            />
          )}
        </DialogContent>
      </Dialog>
      <Dialog open={passwordFor !== null} onOpenChange={(o) => !o && setPasswordFor(null)}>
        <DialogContent
          title={`Set a password for ${passwordFor?.username ?? 'user'}`}
          description="All of this user's sessions end, except your own current session if it is yours."
        >
          {passwordFor && (
            <PasswordForm
              user={passwordFor}
              onDone={(n) => {
                setNotice({ text: `Password set for ${passwordFor.username}; ${n} session(s) ended.` })
                setPasswordFor(null)
              }}
              onCancel={() => setPasswordFor(null)}
            />
          )}
        </DialogContent>
      </Dialog>
    </section>
  )
}

function UserForm({
  user,
  onDone,
  onCancel,
  onConflict,
}: {
  user?: User
  onDone: (u: User) => void | Promise<void>
  onCancel: () => void
  onConflict?: () => void
}) {
  const [username, setUsername] = useState(user?.username ?? '')
  const [displayName, setDisplayName] = useState(user?.display_name ?? '')
  const [role, setRole] = useState<Role>(user?.role ?? 'read-only')
  const [local, setLocal] = useState<Record<string, string>>({})
  const m = useMutation({
    mutationFn: () => {
      if (!user) {
        return apiFetch<User>(api('/users'), {
          method: 'POST',
          json: { username: username.trim(), role, ...(displayName.trim() ? { display_name: displayName.trim() } : {}) },
        })
      }
      // Merge patch: send only what changed; an emptied display name clears it (null).
      const patch: Record<string, unknown> = {}
      if (username.trim() !== user.username) patch.username = username.trim()
      if (role !== user.role) patch.role = role
      if (displayName.trim() !== (user.display_name ?? '')) patch.display_name = displayName.trim() || null
      return apiFetch<User>(api(`/users/${user.id}`), { method: 'PATCH', headers: { 'If-Match': `"${user.version}"` }, json: patch })
    },
    onSuccess: (u) => onDone(u),
    onError: (e) => {
      if (e instanceof ApiError && e.code === 'version_conflict') onConflict?.()
    },
  })
  const { fields, form } = splitErrors(m.error, ['username', 'display_name', 'role'])
  const err = { ...fields, ...local }
  const conflict = m.error instanceof ApiError && m.error.code === 'version_conflict'
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l: Record<string, string> = {}
        if (!username.trim()) l.username = 'Enter a username.'
        setLocal(l)
        if (!l.username) m.mutate()
      }}
    >
      <FormError
        message={conflict ? 'This user was changed elsewhere. Close this dialog and open it again to see the current values.' : form}
      />
      <Field label="Username" name="username" hint="Lower-case letters, digits, dots, dashes and underscores." error={err.username}>
        {(p) => <Input {...p} autoFocus autoCapitalize="none" spellCheck={false} maxLength={64} value={username} onChange={(e) => setUsername(e.target.value)} />}
      </Field>
      <Field label="Display name (optional)" name="display_name" error={err.display_name}>
        {(p) => <Input {...p} maxLength={100} value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}
      </Field>
      <Field label="Role" name="role" error={err.role}>
        {(p) => (
          <Select {...p} value={role} onChange={(e) => setRole(e.target.value as Role)}>
            {ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={m.isPending}>
          Cancel
        </Button>
        <Button type="submit" disabled={m.isPending}>
          {m.isPending ? 'Saving…' : user ? 'Save changes' : 'Add user'}
        </Button>
      </div>
    </form>
  )
}

function PasswordForm({ user, onDone, onCancel }: { user: User; onDone: (sessionsEnded: number) => void; onCancel: () => void }) {
  const [pw, setPw] = useState('')
  const m = useMutation({
    mutationFn: () => apiFetch<{ sessions_ended: number }>(api(`/users/${user.id}/password`), { method: 'PUT', json: { password: pw } }),
    onSuccess: (r) => onDone(r.sessions_ended),
  })
  const { fields, form } = splitErrors(m.error, ['password'])
  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        m.mutate()
      }}
    >
      <FormError message={form} />
      <Field label="New password" name="password" error={fields.password}>
        {(p) => <Input {...p} type="password" autoComplete="new-password" autoFocus value={pw} onChange={(e) => setPw(e.target.value)} />}
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={m.isPending}>
          Cancel
        </Button>
        <Button type="submit" disabled={m.isPending || pw === ''}>
          {m.isPending ? 'Saving…' : 'Set password'}
        </Button>
      </div>
    </form>
  )
}
