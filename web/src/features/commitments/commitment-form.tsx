import { useState } from 'react'
import { useForm } from '@tanstack/react-form'
import { Button } from '@/components/ui/button'
import { Input, Select } from '@/components/ui/input'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { StatusBadge } from '@/components/status-badge'
import { ApiError, NetworkError } from '@/lib/api'
import { isVersionConflict } from '@/lib/optimistic-save'
import { type FormValues, formFieldOf, validate } from './form-model'
import { KINDS, PRIORITIES } from './types'

const MONTHS = Array.from({ length: 12 }, (_, i) => new Intl.DateTimeFormat('en', { month: 'long', timeZone: 'UTC' }).format(new Date(Date.UTC(2001, i, 1))))
const LEEWAY_LABEL = { '': 'Nothing recorded', unknown: 'I do not know', none: 'No grace', period: 'A number of days after each due date', deadline: 'Until a fixed date' } as const

/** What the server did with a failed save, as the form shows it: a message per field and a message for the form. */
function explain(err: unknown): { fields: Partial<Record<keyof FormValues, string>>; form?: string } {
  const out: Partial<Record<keyof FormValues, string>> = {}
  if (!(err instanceof ApiError)) return { fields: out, form: err instanceof Error ? err.message : 'Something went wrong.' }
  const other: string[] = []
  for (const e of err.errors) {
    const f = formFieldOf(e.field)
    if (f) out[f] ??= e.message
    // An error the form has no field for stays visible as the form message.
    else other.push(e.message)
  }
  // A refusal with no field at all (a conflict, a rate limit) is shown as the form message.
  if (err.errors.length === 0) return { fields: out, form: err.message }
  return { fields: out, form: other.length ? other.join(' ') : undefined }
}

export interface CommitmentFormProps {
  initial: FormValues
  mode: 'create' | 'edit'
  /** Saves; rejects with the server's error. The form keeps every entry when it does. */
  onSave: (v: FormValues) => Promise<void>
  /** Disables every control (a role without commitments:write). */
  readOnly?: boolean
  /** In edit mode: whether the entries differ from what was loaded, from the caller's point of view (drives the note about occurrences). */
  occurrenceNote?: (v: FormValues) => string | null
  /** After a version conflict: drops the entries and loads what the server holds now. */
  onReload?: () => void
  onCancel: () => void
}

export function CommitmentForm(p: CommitmentFormProps) {
  const [serverErrors, setServerErrors] = useState<Partial<Record<keyof FormValues, string>>>({})
  const [formError, setFormError] = useState<string>()
  const [conflict, setConflict] = useState(false)
  const form = useForm({
    defaultValues: p.initial,
    validators: {
      onSubmit: ({ value }) => {
        const e = validate(value)
        return Object.keys(e).length ? { fields: e } : undefined
      },
    },
    onSubmit: async ({ value }) => {
      setServerErrors({})
      setFormError(undefined)
      setConflict(false)
      try {
        await p.onSave(value)
      } catch (e) {
        const x = explain(e)
        setServerErrors(x.fields)
        setConflict(isVersionConflict(e))
        setFormError(
          e instanceof NetworkError
            ? 'Cannot reach the server. Nothing was saved; what you entered is still here, so you can try again.'
            : isVersionConflict(e)
              ? 'This commitment was changed somewhere else since you opened it. Nothing was saved. Load the latest version to see the changes (your entries here are replaced), or copy what you need first.'
              : (x.form ?? (Object.keys(x.fields).length ? 'The server did not accept the commitment. Nothing was saved; fix the marked fields and save again.' : undefined)),
        )
      }
    },
  })
  const ro = !!p.readOnly

  // A field's message: the client check first, else what the server said. Typing in the field clears the server's message.
  function err(name: keyof FormValues, meta: { errors: unknown[] }): string | undefined {
    const m = meta.errors.find((x) => typeof x === 'string') as string | undefined
    return m ?? serverErrors[name]
  }
  const clear = (name: keyof FormValues) => {
    if (serverErrors[name]) setServerErrors((s) => ({ ...s, [name]: undefined }))
  }

  const text = (name: keyof FormValues, label: string, opts: { hint?: string; mode?: 'decimal' | 'numeric'; max?: number; className?: string; autoFocus?: boolean; placeholder?: string } = {}) => (
    <form.Field name={name}>
      {(f) => (
        <Field label={label} name={name} hint={opts.hint} error={err(name, f.state.meta)} className={opts.className}>
          {(c) => (
            <Input
              {...c}
              value={f.state.value as string}
              disabled={ro}
              autoComplete="off"
              inputMode={opts.mode}
              maxLength={opts.max}
              autoFocus={opts.autoFocus}
              placeholder={opts.placeholder}
              onBlur={f.handleBlur}
              onChange={(e) => {
                clear(name)
                f.handleChange(e.target.value as never)
              }}
            />
          )}
        </Field>
      )}
    </form.Field>
  )

  const select = (name: keyof FormValues, label: string, options: { value: string; label: string }[], hint?: string) => (
    <form.Field name={name}>
      {(f) => (
        <Field label={label} name={name} hint={hint} error={err(name, f.state.meta)}>
          {(c) => (
            <Select
              {...c}
              value={f.state.value as string}
              disabled={ro}
              onChange={(e) => {
                clear(name)
                f.handleChange(e.target.value as never)
              }}
            >
              {options.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
    </form.Field>
  )

  const legend = 'text-sm font-semibold'
  const box = 'space-y-3 rounded-md border border-border bg-surface p-3 sm:p-4'

  return (
    <form
      noValidate
      aria-label={p.mode === 'create' ? 'New commitment' : 'Edit commitment'}
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault()
        e.stopPropagation()
        void form.handleSubmit()
      }}
    >
      {formError && <StatusBadge status="save_failed" />}
      <FormError message={formError} />
      {conflict && p.onReload && (
        <Button type="button" variant="outline" onClick={p.onReload}>
          Load the latest version
        </Button>
      )}

      <fieldset className={box}>
        <legend className={legend}>What it is</legend>
        {text('name', 'Name', { max: 100, autoFocus: p.mode === 'create' && !ro })}
        {select('kind', 'Kind', KINDS)}
        {select('priority', 'Priority', [{ value: '', label: 'Not prioritised yet' }, ...PRIORITIES.map((x) => ({ value: x, label: x.charAt(0).toUpperCase() + x.slice(1) }))])}
      </fieldset>

      <fieldset className={box}>
        <legend className={legend}>Amount</legend>
        <div className="grid gap-3 sm:grid-cols-2">
          {text('amount', 'Amount per due date', { mode: 'decimal', hint: 'A plain decimal such as 12.50. It is kept exactly as you type it.' })}
          {text('currency', 'Currency', { max: 3, hint: 'Three capital letters, for example EUR. Amounts are never converted.', placeholder: 'EUR' })}
        </div>
        {text('tolerance', 'Match tolerance (optional)', {
          mode: 'decimal',
          hint: 'How far short of the amount a bank payment may fall and still be confirmed automatically. Same currency. Empty means only the exact amount is confirmed automatically.',
        })}
      </fieldset>

      <fieldset className={box}>
        <legend className={legend}>When it falls due</legend>
        {select('frequency', 'Repeats', [{ value: 'monthly', label: 'Every so many months, on a day of the month' }, { value: 'annual', label: 'Once a year, on a date' }])}
        <form.Subscribe selector={(s) => s.values.frequency}>
          {(freq) => (
            <div className="grid gap-3 sm:grid-cols-2">
              {freq === 'monthly' ? text('interval', 'Every how many months', { mode: 'numeric', hint: '1 is every month. Months are counted from the start date’s month.' }) : select('month', 'Month', MONTHS.map((m, i) => ({ value: String(i + 1), label: m })))}
              {text('day', 'Day of the month', { mode: 'numeric', hint: 'A day past the end of a shorter month falls on its last day; 29 February falls on 28 February in other years.' })}
            </div>
          )}
        </form.Subscribe>
        <div className="grid gap-3 sm:grid-cols-2">
          {text('startDate', 'Start date', { hint: 'YYYY-MM-DD. A due date before it is not an occurrence.', placeholder: 'YYYY-MM-DD' })}
          {text('endDate', 'End date (optional)', { hint: 'YYYY-MM-DD, the last day it applies, inclusive. Empty runs on.', placeholder: 'YYYY-MM-DD' })}
        </div>
      </fieldset>

      <fieldset className={box}>
        <legend className={legend}>Grace after the due date (optional)</legend>
        <p className="text-xs text-muted-foreground">What you know about the grace the creditor allows. Ledger never guesses it; “nothing recorded” is different from “I do not know” and from “no grace”.</p>
        {select('leewayKind', 'Grace', Object.entries(LEEWAY_LABEL).map(([value, label]) => ({ value, label })))}
        <form.Subscribe selector={(s) => s.values.leewayKind}>
          {(k) => (
            <>
              {k === 'period' && text('leewayDays', 'Days of grace', { mode: 'numeric', hint: '1 to 365 days after each due date.' })}
              {k === 'deadline' && text('leewayUntil', 'Grace lasts until', { hint: 'YYYY-MM-DD', placeholder: 'YYYY-MM-DD' })}
              {k !== '' && (
                <>
                  {text('conditions', 'Conditions (optional)', { max: 500 })}
                  {text('consequences', 'Consequences of paying late (optional)', { max: 500 })}
                  {text('source', 'Where you know this from (optional)', { max: 200 })}
                </>
              )}
            </>
          )}
        </form.Subscribe>
      </fieldset>

      <fieldset className={box}>
        <legend className={legend}>Notes</legend>
        <form.Field name="notes">
          {(f) => (
            <Field label="Notes (optional)" name="notes" hint="Up to 2000 characters." error={err('notes', f.state.meta)}>
              {(c) => (
                <textarea
                  {...c}
                  rows={3}
                  disabled={ro}
                  value={f.state.value}
                  className="w-full rounded-md border border-border-control bg-surface px-3 py-2 text-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ring disabled:opacity-50 aria-[invalid=true]:border-status-error"
                  onChange={(e) => {
                    clear('notes')
                    f.handleChange(e.target.value)
                  }}
                />
              )}
            </Field>
          )}
        </form.Field>
      </fieldset>

      {p.mode === 'edit' && p.occurrenceNote && (
        <form.Subscribe selector={(s) => s.values}>
          {(v) => {
            const note = p.occurrenceNote!(v)
            return (
              <fieldset className={box} disabled={ro}>
                <legend className={legend}>How this edit reaches occurrences</legend>
                <p className="text-sm text-muted-foreground" data-testid="occurrence-note">
                  {note}
                </p>
                {text('effectiveFrom', 'Apply changed terms from (optional)', {
                  hint: 'YYYY-MM-DD. Empty means today in the reporting time zone, so overdue unpaid occurrences keep their old terms. An earlier date also rewrites earlier unpaid occurrences; paid ones never change.',
                  placeholder: 'YYYY-MM-DD',
                })}
              </fieldset>
            )
          }}
        </form.Subscribe>
      )}

      {!ro && (
        <form.Subscribe selector={(s) => s.isSubmitting}>
          {(saving) => (
            <div className="flex flex-wrap items-center gap-2">
              <Button type="submit" disabled={saving}>
                {saving ? 'Saving…' : p.mode === 'create' ? 'Create commitment' : 'Save changes'}
              </Button>
              <Button type="button" variant="outline" disabled={saving} onClick={p.onCancel}>
                Cancel
              </Button>
              {saving && <StatusBadge status="pending" />}
            </div>
          )}
        </form.Subscribe>
      )}
    </form>
  )
}
