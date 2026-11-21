import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { StatusBadge } from '@/components/status-badge'
import { useAnnounce } from '@/components/announcer'
import { completeConsent, connectionsKey, refresh } from './api'
import type { BankConnection } from './types'

/** What the bank's redirect carried. The code is single-use and sensitive: it lives in memory only, and the address is cleaned at once. */
export interface Redirect {
  state?: string
  code?: string
  error?: string
}

/**
 * The page the bank sends the user back to (banking.redirect_url). It completes the consent exactly once with the
 * state and code from the address, removes them from the address bar and history, and says what happened:
 * connected, refused by the bank, a temporary problem (try again, the same redirect still works) or an approval
 * that was already used. Nothing is shown as connected until the server says so.
 */
export function ConsentCallbackPage({ redirect }: { redirect: Redirect }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const announce = useAnnounce()
  // Captured once: the address is rewritten below, and a re-render must never complete twice.
  const [params] = useState(redirect)
  const started = useRef(false)
  const complete = useMutation({
    mutationFn: () => completeConsent({ state: params.state!, ...(params.error ? { error: params.error } : { code: params.code ?? '' }) }),
    onSuccess: (c) => {
      announce(`${c.institution.name} is connected.`)
      void qc.invalidateQueries({ queryKey: connectionsKey })
    },
    onError: (e) => announce(e instanceof Error ? e.message : 'Could not finish connecting.', 'assertive'),
  })
  const usable = !!params.state && (!!params.code || !!params.error)

  useEffect(() => {
    if (!usable || started.current) return
    started.current = true
    void navigate({ to: '/connections/callback', search: {}, replace: true })
    complete.mutate()
  }, [usable, navigate, complete])

  return (
    <section className="space-y-4" aria-labelledby="callback-title">
      <h1 id="callback-title" className="text-2xl font-semibold">
        Finishing the bank connection
      </h1>
      {!usable && (
        <div className="space-y-2">
          <p>This page finishes connecting a bank. It only works when your bank sends you back to it after you approve access, and it has nothing to finish now.</p>
          <Button asChild>
            <Link to="/connections">Go to bank connections</Link>
          </Button>
        </div>
      )}
      {usable && complete.isPending && (
        <p role="status" className="text-muted-foreground">
          Completing the approval with your bank… Nothing is connected until this finishes.
        </p>
      )}
      {usable && complete.isError && <Failure error={complete.error} refused={!!params.error} retry={() => complete.mutate()} />}
      {complete.isSuccess && <Done c={complete.data} />}
    </section>
  )
}

function Failure({ error, refused, retry }: { error: Error; refused: boolean; retry: () => void }) {
  const api = error instanceof ApiError ? error : null
  const temporary = api?.code === 'bank_unavailable' || api?.code === 'bank_rate_limited'
  if (temporary) {
    return (
      <div className="space-y-2" data-callback="temporary">
        <StatusBadge status="temporary" />
        <p>
          Ledger could not reach the bank or provider to finish the approval{api?.retryAfter ? ` (try again in about ${api.retryAfter} seconds)` : ''}. Nothing was
          connected, and <strong>your approval is not lost</strong>: trying again can work.
        </p>
        <Button onClick={retry}>Try again</Button>
      </div>
    )
  }
  const used = api?.code === 'consent_not_found'
  return (
    <div className="space-y-2" data-callback={used ? 'used' : 'failed'}>
      <StatusBadge status="error" label={refused ? 'Approval refused' : used ? 'Approval already used' : 'Could not connect'} />
      <FormError message={error.message} />
      <p className="text-sm">
        {used
          ? 'This approval link was already completed, or a newer attempt replaced it. Nothing new was connected; check the list below before starting again.'
          : 'Nothing was connected. Start again from the bank connections page.'}
      </p>
      <Button asChild>
        <Link to="/connections">Go to bank connections</Link>
      </Button>
    </div>
  )
}

function Done({ c }: { c: BankConnection }) {
  const qc = useQueryClient()
  const announce = useAnnounce()
  const fetchNow = useMutation({
    mutationFn: () => refresh(c.id),
    onSuccess: (fresh) => {
      announce(fresh.last_refresh_run?.outcome === 'succeeded' ? `${fresh.institution.name} accounts fetched.` : `${fresh.institution.name}: the first fetch did not finish.`)
      void qc.invalidateQueries({ queryKey: connectionsKey })
    },
  })
  const result = fetchNow.data
  const run = result?.last_refresh_run
  return (
    <div className="space-y-3" data-callback="connected">
      <p role="status" className="font-medium">
        {c.institution.name} is connected. No accounts or balances have been fetched yet.
      </p>
      <div className="flex flex-wrap gap-2">
        <Button disabled={fetchNow.isPending || fetchNow.isSuccess} onClick={() => fetchNow.mutate()}>
          {fetchNow.isPending ? 'Fetching accounts…' : 'Fetch accounts now'}
        </Button>
        <Button asChild variant="outline">
          <Link to="/connections">Go to bank connections</Link>
        </Button>
      </div>
      {fetchNow.isError && <FormError message={fetchNow.error.message} />}
      {run && (
        <p role="status" className="text-sm">
          {run.outcome === 'succeeded' && `Fetched ${result.accounts.length} account(s).`}
          {run.outcome === 'partial' && `Fetched some accounts; ${run.accounts_failed} failed. See bank connections.`}
          {run.outcome !== 'succeeded' && run.outcome !== 'partial' && 'The first fetch did not finish. Earlier data is unchanged (there is none yet); try again from bank connections.'}
        </p>
      )}
      <p className="text-xs text-muted-foreground">
        Ledger also refreshes connected banks in the background (<Mono>banking.refresh</Mono>).
      </p>
    </div>
  )
}
