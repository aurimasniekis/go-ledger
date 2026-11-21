import { useState } from 'react'
import { ApiError } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/input'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent } from '@/components/ui/dialog'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { listInstitutions, startConnection, useApplications } from './api'
import { goToBank } from './redirect'
import type { BankApplication, BankInstitution } from './types'

const ACCESS_TEXT: Record<BankApplication['account_access'], string> = {
  all: 'Every account you approve at the bank can be connected.',
  linked_only: 'Only accounts linked to this application at the provider can be connected; other accounts will not appear.',
}

/**
 * The connect flow: choose the provider application, a country and a bank, then go to the bank to approve access.
 * Ledger never asks for or sees your bank login: you sign in at the bank. Nothing is connected until you return
 * from the bank and the redirect is completed (the callback page).
 */
export function ConnectDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        title="Connect a bank"
        description="You will approve access at your bank. Ledger never asks for or sees your bank login, and only reads balances and transactions."
      >
        <ConnectForm onCancel={() => onOpenChange(false)} />
      </DialogContent>
    </Dialog>
  )
}

function ConnectForm({ onCancel }: { onCancel: () => void }) {
  const apps = useApplications()
  const [appName, setAppName] = useState('')
  const [country, setCountry] = useState('')
  const [banks, setBanks] = useState<BankInstitution[] | null>(null)
  const [bankId, setBankId] = useState('')
  const [finding, setFinding] = useState(false)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState<string>()
  const [local, setLocal] = useState<Record<string, string>>({})

  if (apps.isPending) {
    return (
      <p role="status" className="text-sm text-muted-foreground">
        Loading the configured banking providers…
      </p>
    )
  }
  if (!apps.data) {
    return (
      <div className="space-y-3">
        <FormError message={`Could not load the banking providers: ${apps.error?.message ?? 'unknown error'}`} />
        <Button variant="outline" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }
  if (apps.data.length === 0) {
    return (
      <div className="space-y-3">
        <p className="text-sm">
          No banking provider is configured, so no bank can be connected. An administrator configures one under <Mono>banking.enable_banking</Mono> in the server
          configuration.
        </p>
        <Button variant="outline" onClick={onCancel}>
          Close
        </Button>
      </div>
    )
  }
  const only = apps.data.length === 1 ? apps.data[0] : undefined
  const app = only ?? apps.data.find((a) => a.name === appName)

  const problem = (e: unknown) => {
    if (e instanceof ApiError && e.code === 'bank_connect_unavailable')
      return 'Connecting a bank is not set up on this server: its administrator must set banking.redirect_url.'
    if (e instanceof ApiError && (e.code === 'bank_unavailable' || e.code === 'bank_rate_limited'))
      return `${e.message}${e.retryAfter ? ` (try again in about ${e.retryAfter} seconds)` : ''}`
    return e instanceof Error ? e.message : 'Something went wrong.'
  }

  async function find() {
    const l: Record<string, string> = {}
    if (!app) l.application = 'Choose a provider application.'
    if (!/^[A-Z]{2}$/.test(country)) l.country = 'Enter the two-letter country code of the bank, such as DE.'
    setLocal(l)
    if (Object.keys(l).length || !app) return
    setFinding(true)
    setError(undefined)
    setBanks(null)
    setBankId('')
    try {
      setBanks(await listInstitutions(app.name, country))
    } catch (e) {
      setError(problem(e))
    } finally {
      setFinding(false)
    }
  }

  async function start() {
    if (!app) return
    if (!bankId) {
      setLocal({ institution: 'Choose a bank.' })
      return
    }
    setLocal({})
    setStarting(true)
    setError(undefined)
    try {
      const s = await startConnection({ application: app.name, institution_id: bankId, country })
      goToBank(s.authorization_url)
    } catch (e) {
      setError(problem(e))
      setStarting(false)
    }
  }

  return (
    <form
      className="space-y-4"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        void (banks ? start() : find())
      }}
    >
      <FormError message={error} />
      {!only ? (
        <Field label="Provider application" name="application" error={local.application} hint={app ? ACCESS_TEXT[app.account_access] : 'Each application has its own mode and reach.'}>
          {(p) => (
            <Select {...p} value={appName} onChange={(e) => (setAppName(e.target.value), setBanks(null))}>
              <option value="">Choose…</option>
              {apps.data.map((a) => (
                <option key={a.name} value={a.name}>
                  {a.name} ({a.mode} mode{a.environment === 'sandbox' ? ', sandbox' : ''})
                </option>
              ))}
            </Select>
          )}
        </Field>
      ) : (
        <p className="text-sm text-muted-foreground">
          Provider application <Mono>{only.name}</Mono> ({only.mode} mode
          {only.environment === 'sandbox' ? ', sandbox with simulated banks' : ''}). {ACCESS_TEXT[only.account_access]}
        </p>
      )}
      <Field label="Country of the bank" name="country" hint="Two letters, such as DE or FI." error={local.country}>
        {(p) => (
          <Input
            {...p}
            maxLength={2}
            autoComplete="off"
            autoCapitalize="characters"
            className="w-24 uppercase"
            value={country}
            onChange={(e) => (setCountry(e.target.value.toUpperCase()), setBanks(null))}
          />
        )}
      </Field>
      {banks && (
        <Field
          label="Bank"
          name="institution"
          error={local.institution}
          hint={banks.length === 0 ? 'The provider offers no banks in that country.' : `${banks.length} bank${banks.length === 1 ? '' : 's'} available.`}
        >
          {(p) => (
            <Select {...p} value={bankId} onChange={(e) => setBankId(e.target.value)} disabled={banks.length === 0}>
              <option value="">Choose…</option>
              {banks.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.name}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" variant="outline" onClick={onCancel} disabled={starting}>
          Cancel
        </Button>
        {banks ? (
          <Button type="submit" disabled={starting || banks.length === 0}>
            {starting ? 'Opening your bank…' : 'Continue to your bank'}
          </Button>
        ) : (
          <Button type="submit" disabled={finding}>
            {finding ? 'Finding banks…' : 'Find banks'}
          </Button>
        )}
      </div>
    </form>
  )
}
