import { useId, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'
import { isNetworkError } from '@/lib/connection'
import { formatDate, formatInstant } from '@/lib/format'
import { Button } from '@/components/ui/button'
import { Amount } from '@/components/formatted'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { Notice } from '@/components/notice'
import { StatusBadge, type Status } from '@/components/status-badge'
import { useAnnounce } from '@/components/announcer'
import { connectionsKey, disconnect, reconnect, refresh } from './api'
import { accountState, BALANCE_LABEL, diagnose, type AccountState, type Diagnosis } from './diagnosis'
import { goToBank } from './redirect'
import type { BankAccount, BankConnection, BankError } from './types'

const KIND_TEXT: Record<BankError['kind'], string> = {
  reconnect_required: 'consent ended or withdrawn',
  rate_limited: 'the bank asked for a pause',
  retryable: 'the bank could not be reached',
  permanent: 'the provider refused the request',
  canceled: 'the request was cancelled',
}
const code = (e: BankError | null) => (e?.code ? <> (<Mono>{e.code}</Mono>)</> : null)

/** One short sentence for the outcome of a manual refresh, for the live region. */
function outcomeText(c: BankConnection): string {
  const run = c.last_refresh_run
  const name = c.institution.name
  if (c.refresh_running) return `${name}: the refresh is still running at the bank. This page updates when it ends.`
  if (!run) return `${name}: no refresh result.`
  if (run.outcome === 'succeeded') return `${name} refreshed.`
  if (run.outcome === 'partial') return `${name} refreshed, but ${run.accounts_failed} account(s) failed. Their earlier data is kept.`
  const e = run.error
  if (e?.kind === 'reconnect_required') return `${name}: the bank consent has ended. Reconnect the bank; retrying will not help.`
  if (e?.kind === 'permanent') return `${name}: refresh refused by the provider. Retrying will not help. Earlier data is kept.`
  if (e) return `${name}: could not refresh because ${KIND_TEXT[e.kind]}. Earlier data is kept; try again later.`
  return `${name}: the refresh did not finish. Earlier data is kept.`
}

function refreshProblem(e: unknown): string {
  if (e instanceof ApiError) {
    if (e.code === 'refresh_in_progress') return 'A refresh of this connection is already running. Wait a moment, then try again.'
    if (e.code === 'connection_not_active') return 'This connection can no longer be refreshed: it needs to be reconnected first.'
    return e.message
  }
  if (isNetworkError(e)) return 'Cannot reach Ledger, so nothing was refreshed. Earlier data is unchanged.'
  return e instanceof Error ? e.message : 'The refresh failed.'
}

const ACCOUNT_BADGE: Record<AccountState, { status: Status; label?: string }> = {
  fresh: { status: 'fresh' },
  stale: { status: 'stale', label: 'Stale' },
  never: { status: 'never_fetched' },
  reconnect: { status: 'reconnect' },
  temporary: { status: 'temporary' },
  rejected: { status: 'error', label: 'Refresh failed' },
  disconnected: { status: 'disconnected', label: 'Bank disconnected' },
}

function AccountRow({ a }: { a: BankAccount }) {
  const f = a.freshness
  const state = accountState(a)
  return (
    <li className="space-y-2 rounded-md border border-border p-3" data-account-state={state}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <p className="font-medium">
            {a.name || 'Account'}
            {a.identifier_hint && <span className="ml-2 font-normal text-muted-foreground"><span aria-hidden>••••</span><span className="sr-only">ending </span> <Mono>{a.identifier_hint}</Mono></span>}
          </p>
          <p className="text-xs text-muted-foreground">
            {[a.product, a.currency === 'XXX' ? 'several currencies' : a.currency].filter(Boolean).join(' · ')}
          </p>
        </div>
        {a.no_longer_reported ? <StatusBadge status="stale" label="No longer reported by the bank" /> : <StatusBadge {...ACCOUNT_BADGE[state]} />}
      </div>
      {a.balances.length === 0 ? (
        <p className="text-sm text-muted-foreground">The bank has not reported a balance for this account. Ledger does not guess one.</p>
      ) : (
        <ul aria-label={`Balances of ${a.name || 'the account'}`} className="space-y-0.5 text-sm">
          {a.balances.map((b) => (
            <li key={b.type} className="flex flex-wrap items-baseline gap-x-2">
              <span className="text-muted-foreground">{BALANCE_LABEL[b.type] ?? b.type}</span>
              <Amount amount={b.amount.amount} currency={b.amount.currency} className="font-medium tabular-nums" />
              <span className="text-xs text-muted-foreground">
                as obtained <time dateTime={b.obtained_at}>{formatInstant(b.obtained_at)}</time>
                {b.reference_date && <> (bank date {formatDate(b.reference_date)})</>}
              </span>
            </li>
          ))}
        </ul>
      )}
      <dl className="grid gap-x-4 gap-y-0.5 text-xs text-muted-foreground sm:grid-cols-2">
        <div>
          <dt className="inline">Last successful refresh: </dt>
          <dd className="inline text-foreground">{f.last_success_at ? <time dateTime={f.last_success_at}>{formatInstant(f.last_success_at)}</time> : 'never'}</dd>
        </div>
        <div>
          <dt className="inline">Last attempt: </dt>
          <dd className="inline text-foreground">{f.last_attempt_at ? <time dateTime={f.last_attempt_at}>{formatInstant(f.last_attempt_at)}</time> : 'none yet'}</dd>
        </div>
        {a.transactions_through && (
          <div>
            <dt className="inline">Transactions through: </dt>
            <dd className="inline text-foreground">{formatDate(a.transactions_through)}</dd>
          </div>
        )}
      </dl>
      {f.last_error && state !== 'fresh' && (
        <p className="text-xs">
          Last error: {KIND_TEXT[f.last_error.kind]}
          {code(f.last_error)}. The figures above are from the last successful refresh.
        </p>
      )}
      {state === 'stale' && !f.last_error && <p className="text-xs">Older than 24 hours: the figures above may be out of date.</p>}
      {f.notice_code === 'transactions_rejected' && (
        <p className="text-xs">Some transactions in the last refresh were rejected as invalid and are not shown.</p>
      )}
    </li>
  )
}

function DiagnosisPanel({ c, d }: { c: BankConnection; d: Diagnosis }) {
  switch (d.kind) {
    case 'reconnect':
      return (
        <div data-diagnosis="reconnect" className="space-y-1 rounded-md border-2 border-status-error p-3">
          <StatusBadge status="reconnect" />
          <p className="text-sm">
            The bank consent has ended or was withdrawn{code(d.error)}. <strong>Retrying will not help</strong>: approve access again at your bank. The accounts, balances and
            transactions below stay as they were at the last successful refresh.
          </p>
        </div>
      )
    case 'temporary':
      return (
        <div data-diagnosis="temporary" className="space-y-1 rounded-md border border-border-control p-3">
          <StatusBadge status="temporary" />
          <p className="text-sm">
            {d.rateLimited ? 'The bank or provider asked Ledger to pause' : 'Ledger could not reach the bank or provider'}
            {code(d.error)}. Your consent is fine and nothing was lost; <strong>trying again later can work</strong>. The balances below are from the last successful refresh.
            {d.nextAttemptAt && (
              <>
                {' '}Ledger will try again by itself after <time dateTime={d.nextAttemptAt}>{formatInstant(d.nextAttemptAt)}</time>.
              </>
            )}
          </p>
        </div>
      )
    case 'rejected':
      return (
        <div data-diagnosis="rejected" className="space-y-1 rounded-md border border-border-control p-3">
          <StatusBadge status="error" label="Refresh failed" />
          <p className="text-sm">
            The provider refused the request{code(d.error)}. Trying again unchanged will not help, and neither will reconnecting by itself. Earlier data is kept.
          </p>
        </div>
      )
    case 'pending':
      return (
        <div data-diagnosis="pending" className="space-y-1 rounded-md border border-border-control p-3">
          <StatusBadge status="awaiting" />
          <p className="text-sm">Access was asked for but not approved here yet. No accounts are connected. Remove this entry and connect again to start over.</p>
        </div>
      )
    case 'consent_failed':
      return (
        <div data-diagnosis="consent_failed" className="space-y-1 rounded-md border border-border-control p-3">
          <StatusBadge status="error" label="Could not connect" />
          <p className="text-sm">
            The bank or provider refused, or the approval did not finish{code(d.error)}. Nothing was connected. Remove this entry and connect again.
          </p>
        </div>
      )
    case 'disconnected':
      return (
        <div data-diagnosis="disconnected" className="space-y-1 rounded-md border border-border-control p-3">
          <StatusBadge status="disconnected" label="Bank disconnected" />
          <p className="text-sm">
            This bank is disconnected: nothing new is fetched. Stored accounts, balances and transactions stay as history, as of the times shown. Reconnect to fetch again.
          </p>
        </div>
      )
    case 'ok':
      return d.partial ? (
        <p data-diagnosis="partial" className="text-sm">
          The last refresh finished for some accounts and failed for {c.last_refresh_run?.accounts_failed ?? 'some'}. Each account below says which; failed accounts keep their earlier data.
        </p>
      ) : null
  }
}

/** One bank connection: what it is, what it needs from the user, and each account's freshness. */
export function ConnectionCard({ c, canWrite }: { c: BankConnection; canWrite: boolean }) {
  const qc = useQueryClient()
  const announce = useAnnounce()
  const headingId = useId()
  const [disconnecting, setDisconnecting] = useState(false)
  const d = diagnose(c)
  const invalidate = () => qc.invalidateQueries({ queryKey: connectionsKey })

  const doRefresh = useMutation({
    mutationFn: () => refresh(c.id),
    onSuccess: async (fresh) => {
      announce(outcomeText(fresh), fresh.last_refresh_run?.outcome === 'succeeded' || fresh.refresh_running ? 'polite' : 'assertive')
      await invalidate()
    },
    onError: async (e) => {
      announce(refreshProblem(e), 'assertive')
      await invalidate()
    },
  })
  const doReconnect = useMutation({
    mutationFn: async () => goToBank((await reconnect(c.id)).authorization_url),
  })

  const canRefresh = c.status === 'active' && d.kind !== 'reconnect'
  const needsReconnect = d.kind === 'reconnect'
  const canReconnect = needsReconnect || d.kind === 'disconnected' || (d.kind === 'ok' && c.consent_expires_soon)
  const reconnectPrimary = needsReconnect || d.kind === 'disconnected'
  const running = c.refresh_running
  const busy = doRefresh.isPending || doReconnect.isPending || running
  const connectedFor = c.mode === 'developer' ? 'Developer mode: only accounts linked to the application at the provider appear.' : null

  return (
    <article aria-labelledby={headingId} className="space-y-3 rounded-lg border border-border bg-surface p-4" data-connection-status={c.status}>
      <header className="flex flex-wrap items-start justify-between gap-2">
        <div>
          <h2 id={headingId} className="text-lg font-semibold">
            {c.institution.name}
          </h2>
          <p className="text-xs text-muted-foreground">
            {c.institution.country} · application <Mono>{c.application}</Mono> · {c.mode} mode{c.environment === 'sandbox' ? ' · sandbox (simulated banks)' : ''}
          </p>
          {connectedFor && <p className="text-xs text-muted-foreground">{connectedFor}</p>}
        </div>
        {c.consent_valid_until && c.status !== 'disconnected' && (
          <p className="text-xs text-muted-foreground">
            Consent valid until <time dateTime={c.consent_valid_until}>{formatInstant(c.consent_valid_until)}</time>
          </p>
        )}
      </header>

      <DiagnosisPanel c={c} d={d} />

      {d.kind === 'ok' && c.consent_expires_soon && c.consent_valid_until && (
        <Notice>
          The bank consent ends on <time dateTime={c.consent_valid_until}>{formatInstant(c.consent_valid_until)}</time>. Reconnect before then to avoid a gap in fresh data.
        </Notice>
      )}

      {c.accounts.length > 0 ? (
        <ul aria-label={`Accounts at ${c.institution.name}`} className="space-y-2">
          {c.accounts.map((a) => (
            <AccountRow key={a.id} a={a} />
          ))}
        </ul>
      ) : (
        c.status === 'active' && (
          <p className="text-sm text-muted-foreground">
            {c.last_refresh_run ? 'The bank reported no accounts' + (c.account_access === 'linked_only' ? ' linked to this application.' : '.') : 'No accounts have been fetched yet. Refresh to fetch them.'}
          </p>
        )
      )}

      {doRefresh.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          Refreshing {c.institution.name}… Nothing below has changed yet.
        </p>
      )}
      {running && !doRefresh.isPending && (
        <p role="status" className="text-sm text-muted-foreground">
          A refresh of {c.institution.name} is still running at the bank. This page updates when it ends; nothing below has changed yet.
        </p>
      )}
      {doRefresh.isError && <FormError message={refreshProblem(doRefresh.error)} />}
      {doReconnect.isError && <FormError message={doReconnect.error instanceof Error ? doReconnect.error.message : 'Could not start reconnecting.'} />}

      {canWrite && (
        <div className="flex flex-wrap gap-2">
          {canReconnect && (
            <Button variant={reconnectPrimary ? 'default' : 'outline'} disabled={busy} onClick={() => doReconnect.mutate()}>
              {doReconnect.isPending ? 'Opening your bank…' : reconnectPrimary ? 'Reconnect bank' : 'Reconnect now'}
            </Button>
          )}
          {canRefresh && (
            <Button variant={d.kind === 'temporary' ? 'default' : 'outline'} disabled={busy} onClick={() => doRefresh.mutate()}>
              {doRefresh.isPending || running ? 'Refreshing…' : d.kind === 'temporary' ? 'Try again' : 'Refresh now'}
            </Button>
          )}
          {c.status !== 'disconnected' && (
            <Button variant="outline" disabled={busy} onClick={() => setDisconnecting(true)}>
              {d.kind === 'pending' || d.kind === 'consent_failed' ? 'Remove' : 'Disconnect'}
            </Button>
          )}
        </div>
      )}
      {needsReconnect && canWrite && <p className="text-xs text-muted-foreground">Refreshing is off until you reconnect: it would fail the same way.</p>}

      <ConfirmDialog
        open={disconnecting}
        onOpenChange={setDisconnecting}
        title={`${d.kind === 'pending' || d.kind === 'consent_failed' ? 'Remove' : 'Disconnect'} ${c.institution.name}?`}
        description={
          d.kind === 'pending' || d.kind === 'consent_failed'
            ? 'This entry never connected any accounts. It is marked disconnected and nothing else changes.'
            : 'Ledger asks the provider to end the bank consent and stops fetching. Accounts, balances and transactions already stored stay as history, and payments matched to them stay matched. You can reconnect later.'
        }
        confirmLabel={d.kind === 'pending' || d.kind === 'consent_failed' ? 'Remove entry' : 'Disconnect bank'}
        onConfirm={async () => {
          await disconnect(c)
          announce(`${c.institution.name} disconnected.`)
          await invalidate()
        }}
      />
    </article>
  )
}
