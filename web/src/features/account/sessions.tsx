import { useState } from 'react'
import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch, setCsrfToken } from '@/lib/api'
import { api } from '@/lib/auth'
import type { Page, Session } from '@/lib/types'
import { formatInstant } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { QueryState } from '@/components/query-state'

export function SessionsPage() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const q = useQuery({
    queryKey: ['sessions'],
    queryFn: async () => (await apiFetch<Pick<Page<Session>, 'items'>>(api('/auth/sessions'))).items,
  })
  const [ending, setEnding] = useState<Session | null>(null)

  return (
    <section className="space-y-4">
      <h1 className="text-2xl font-semibold">Sessions</h1>
      <p className="text-sm text-muted-foreground">
        Where you are signed in. Ending a session signs that browser out on its next request.
      </p>
      <QueryState query={q} label="your sessions">
        {(sessions) => (
          <ul className="space-y-3">
            {sessions.map((s) => (
              <li key={s.id} className="flex flex-wrap items-start justify-between gap-3 rounded-md border border-border bg-surface p-3">
                <div className="min-w-0 space-y-1 text-sm">
                  <p className="font-medium break-words">
                    {s.user_agent || 'Unknown browser'}
                    {s.current && (
                      <span className="ml-2 rounded-sm border border-border px-1.5 py-0.5 text-xs font-medium">This session</span>
                    )}
                  </p>
                  <dl className="grid grid-cols-[auto_1fr] gap-x-3 text-xs text-muted-foreground">
                    <dt>Address</dt>
                    <dd>{s.address || 'unknown'}</dd>
                    <dt>Signed in</dt>
                    <dd>{formatInstant(s.created_at)}</dd>
                    <dt>Last active</dt>
                    <dd>{formatInstant(s.last_seen_at)}</dd>
                    <dt>Ends</dt>
                    <dd>
                      {formatInstant(s.idle_expires_at)} if idle, at the latest {formatInstant(s.expires_at)}
                    </dd>
                  </dl>
                </div>
                <Button
                  variant="outline"
                  size="sm"
                  aria-label={
                    s.current
                      ? 'Sign out of this session'
                      : `End session started ${formatInstant(s.created_at)} from ${s.address || 'an unknown address'}`
                  }
                  onClick={() => setEnding(s)}
                >
                  {s.current ? 'Sign out' : 'End session'}
                </Button>
              </li>
            ))}
          </ul>
        )}
      </QueryState>
      <ConfirmDialog
        open={ending !== null}
        onOpenChange={(o) => !o && setEnding(null)}
        title={ending?.current ? 'Sign out of this session?' : 'End this session?'}
        description={
          ending?.current
            ? 'You will be signed out of this browser and taken to the sign-in page.'
            : 'The browser using it is signed out on its next request. This cannot be undone; it would sign in again.'
        }
        confirmLabel={ending?.current ? 'Sign out' : 'End session'}
        onConfirm={async () => {
          const s = ending!
          await apiFetch(api(`/auth/sessions/${s.id}`), { method: 'DELETE' })
          if (s.current) {
            setCsrfToken(null)
            qc.clear()
            await navigate({ to: '/sign-in' })
          } else {
            await qc.invalidateQueries({ queryKey: ['sessions'] })
          }
        }}
      />
    </section>
  )
}
