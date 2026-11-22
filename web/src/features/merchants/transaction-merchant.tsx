import { useId, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { Store } from 'lucide-react'
import { holds, useMe } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { Input, Select } from '@/components/ui/input'
import { DateText } from '@/components/formatted'
import { Field } from '@/components/field'
import { FormError } from '@/components/form-error'
import { Mono } from '@/components/mono'
import { QueryState } from '@/components/query-state'
import { useLoadAll } from '@/features/organisation/api'
import { OrganiseDisclosure } from '@/features/organisation/organise-panel'
import { clearTransactionMerchantReview, merchantsKey, reviewTransactionMerchant, txMerchantKey, useMerchants, useMerchantSources, useTransactionMerchant } from './api'
import { merchantProblem } from './problems'
import type { MerchantSource, TransactionMerchant } from './types'

/** The source's certainty as a percentage from its exact integer (per mille): no floating point. */
export function confidenceText(permille: number): string {
  return `${Math.floor(permille / 10)}.${permille % 10}%`
}

const WHY: Record<TransactionMerchant['state'], string> = {
  confirmed: 'You confirmed the name the source found.',
  corrected: 'You gave this name.',
  rejected: 'You said this transaction has no merchant. Nothing is shown, even if a source identified one.',
  reliable: 'A source identified this merchant with high confidence and you have not reviewed it.',
  uncertain: 'A source suggested a merchant but with low confidence, so no name is shown until you confirm or correct it.',
  ambiguous: 'A source found several possible merchants and chose none, so no name is shown.',
  none: 'No source knows a merchant for this transaction, so no name is shown.',
  unavailable: 'A lookup failed and nothing was found earlier, so no name is shown. It is tried again later.',
  not_looked_up: 'No lookup has been made for this transaction yet, so no name is shown.',
}

/**
 * The merchant of one bank transaction as a disclosure, read only when opened. A name is shown only when it may be: the
 * person confirmed or corrected it, or a source identified it with high confidence (the server says fallback otherwise, and
 * bank text is never put in its place). No logo is ever fetched: the reference is opaque, the page's content policy does
 * not allow remote images, and loading one would tell a third party about the transaction. The neutral icon is used.
 * Hidden without merchants:read.
 */
export function MerchantDisclosure({ transactionId, subject }: { transactionId: string; subject: string }) {
  const me = useMe()
  const [open, setOpen] = useState(false)
  const panel = useId()
  if (!holds(me.data?.grants, 'merchants:read')) return null
  return (
    <div className="space-y-2">
      <Button variant="ghost" size="sm" className="h-auto min-h-8 py-1 text-foreground underline underline-offset-4" aria-expanded={open} aria-controls={panel} aria-label={`Merchant of ${subject}`} onClick={() => setOpen(!open)}>
        Merchant
      </Button>
      <div id={panel} hidden={!open}>
        {open && <MerchantPanel transactionId={transactionId} canWrite={holds(me.data?.grants, 'merchants:write')} />}
      </div>
    </div>
  )
}

export function MerchantPanel({ transactionId, canWrite }: { transactionId: string; canWrite: boolean }) {
  const q = useTransactionMerchant(transactionId, true)
  const sources = useMerchantSources()
  return <QueryState query={q} label="the merchant">{(m) => <Panel m={m} sources={sources.data ?? []} canWrite={canWrite} />}</QueryState>
}

function Panel({ m, sources, canWrite }: { m: TransactionMerchant; sources: MerchantSource[]; canWrite: boolean }) {
  const qc = useQueryClient()
  const [form, setForm] = useState<'correct' | null>(null)
  const [saved, setSaved] = useState<string>()
  const e = m.enrichment
  const shown = !m.fallback && m.name !== null
  // The licence asks for the source's attribution wherever an identity from that source is shown: a corrected name is the person's own.
  const source = shown && m.state !== 'corrected' ? sources.find((s) => s.name === e?.source) : undefined
  const suggestion = e?.status === 'identified' && !shown ? e : null

  const done = (text: string) => async (r: TransactionMerchant) => {
    qc.setQueryData(txMerchantKey(m.transaction_id), r)
    await qc.invalidateQueries({ queryKey: merchantsKey })
    setForm(null)
    setSaved(text)
  }
  const confirm = useMutation({ mutationFn: () => reviewTransactionMerchant(m.transaction_id, { decision: 'confirmed' }), onMutate: () => setSaved(undefined), onSuccess: done('Confirmed.') })
  const reject = useMutation({ mutationFn: () => reviewTransactionMerchant(m.transaction_id, { decision: 'rejected' }), onMutate: () => setSaved(undefined), onSuccess: done('Marked as having no merchant.') })
  const clear = useMutation({ mutationFn: () => clearTransactionMerchantReview(m.transaction_id), onMutate: () => setSaved(undefined), onSuccess: done('Your decision was taken back.') })
  const busy = confirm.isPending || reject.isPending || clear.isPending
  const error = confirm.error ?? reject.error ?? clear.error
  const canConfirm = e?.status === 'identified' && (m.state === 'uncertain' || m.state === 'reliable' || m.confirmation_outdated)

  return (
    <div className="space-y-3 rounded-md border border-border bg-surface p-3 text-sm" data-state={m.state}>
      <div className="flex items-start gap-2">
        <Store aria-hidden className="mt-0.5 size-5 shrink-0 text-muted-foreground" />
        <div className="min-w-0 space-y-0.5">
          {shown ? <p className="font-medium break-words" data-testid="merchant-name">{m.name}</p> : <p className="font-medium text-muted-foreground" data-testid="merchant-name">No merchant name shown</p>}
          <p className="text-xs text-muted-foreground">{WHY[m.state]}</p>
          {shown && m.state === 'reliable' && e?.confidence_permille != null && (
            <p className="text-xs text-muted-foreground">Identified by {e.source}, confidence {confidenceText(e.confidence_permille)}.</p>
          )}
          {source?.attribution && <p className="text-xs text-muted-foreground" data-testid="attribution">{source.attribution}</p>}
        </div>
      </div>
      {m.confirmation_outdated && (
        <p role="note" className="text-xs">The source now reports a different answer than the one you confirmed, so your confirmation no longer applies. Review it.</p>
      )}
      {m.needs_review && !m.confirmation_outdated && <p role="note" className="text-xs">This needs your review.</p>}
      {suggestion && (
        <p className="text-xs text-muted-foreground" data-testid="suggestion">
          Suggested by {suggestion.source}, not used until you confirm it: {suggestion.name}
          {suggestion.confidence_permille != null && `, confidence ${confidenceText(suggestion.confidence_permille)}`}.
        </p>
      )}
      {e?.status === 'ambiguous' && e.candidates.length > 0 && (
        <p className="text-xs text-muted-foreground" data-testid="candidates">Possible merchants (none chosen by the source): {e.candidates.join(', ')}.</p>
      )}
      {m.review && (
        <p className="text-xs text-muted-foreground" data-testid="review">
          Your decision: {m.review.decision}
          {m.review.name ? ` (${m.review.name})` : ''}, <DateText value={m.review.decided_at} />
          {m.review_inherited && <>, inherited from the pending transaction this booking replaced (<Mono>{m.review.transaction_id}</Mono>): clear it there.</>}
        </p>
      )}
      {e?.last_failed_at && <p className="text-xs text-muted-foreground">The last lookup failed at <DateText value={e.last_failed_at} />; the earlier answer is kept.</p>}
      {e?.retrieved_at && <p className="text-xs text-muted-foreground">Source answer from <DateText value={e.retrieved_at} />.</p>}

      <FormError message={error ? merchantProblem(error) : undefined} />
      {saved && <p role="status" className="text-xs font-medium">{saved}</p>}
      {busy && <p role="status" className="text-xs text-muted-foreground">Saving… not saved until the server confirms.</p>}
      {shown && m.merchant_id && <OrganiseDisclosure objectType="merchant" objectId={m.merchant_id} subject={`the merchant ${m.name}`} />}
      {canWrite ? (
        <div className="space-y-3">
          <div className="flex flex-wrap gap-2">
            {canConfirm && <Button size="sm" variant="outline" disabled={busy} onClick={() => confirm.mutate()}>{confirm.isPending ? 'Saving…' : 'Confirm this merchant'}</Button>}
            <Button size="sm" variant="outline" aria-expanded={form === 'correct'} disabled={busy} onClick={() => setForm(form ? null : 'correct')}>Correct the merchant</Button>
            {m.state !== 'rejected' && <Button size="sm" variant="outline" disabled={busy} onClick={() => reject.mutate()}>{reject.isPending ? 'Saving…' : 'This is not a merchant'}</Button>}
            {m.review && !m.review_inherited && <Button size="sm" variant="outline" disabled={busy} onClick={() => clear.mutate()}>{clear.isPending ? 'Saving…' : 'Take my decision back'}</Button>}
          </div>
          {form === 'correct' && <CorrectForm m={m} onDone={done('Corrected.')} onCancel={() => setForm(null)} />}
        </div>
      ) : (
        <p className="text-xs text-muted-foreground">You can read this but not change it: your token or role does not hold merchants:write.</p>
      )}
    </div>
  )
}

/** A correction names the merchant by text or picks an existing record. Either way it is the person's own word and carries no logo. */
function CorrectForm({ m, onDone, onCancel }: { m: TransactionMerchant; onDone: (r: TransactionMerchant) => void | Promise<void>; onCancel: () => void }) {
  const [name, setName] = useState('')
  const [pick, setPick] = useState('')
  const [local, setLocal] = useState<string>()
  const list = useMerchants(false)
  useLoadAll(list)
  const mut = useMutation({
    mutationFn: () => reviewTransactionMerchant(m.transaction_id, pick ? { decision: 'corrected', merchant_id: pick } : { decision: 'corrected', name: name.trim() }),
    onSuccess: onDone,
  })
  return (
    <form
      className="space-y-3 rounded-md border border-border p-3"
      noValidate
      onSubmit={(e) => {
        e.preventDefault()
        const l = pick || name.trim() ? undefined : 'Enter the merchant name, or choose an existing merchant.'
        setLocal(l)
        if (!l) mut.mutate()
      }}
    >
      <FormError message={mut.error ? merchantProblem(mut.error) : undefined} />
      <Field label="Merchant name" name="correct_name" hint="A name that is already an alias of a merchant uses that merchant; otherwise a new merchant is created." error={local}>
        {(p) => <Input {...p} maxLength={200} value={name} disabled={!!pick || mut.isPending} onChange={(e) => setName(e.target.value)} />}
      </Field>
      <Field label="Or an existing merchant" name="correct_pick" hint="Only active merchants.">
        {(p) => (
          <Select {...p} value={pick} disabled={mut.isPending} onChange={(e) => setPick(e.target.value)}>
            <option value="">{list.isPending ? 'Loading…' : 'None chosen'}</option>
            {(list.data ?? []).map((c) => (
              <option key={c.id} value={c.id}>{c.display_name}</option>
            ))}
          </Select>
        )}
      </Field>
      <div className="flex flex-wrap justify-end gap-2">
        <Button type="button" size="sm" variant="outline" onClick={onCancel} disabled={mut.isPending}>Cancel</Button>
        <Button type="submit" size="sm" disabled={mut.isPending}>{mut.isPending ? 'Saving…' : 'Save correction'}</Button>
      </div>
    </form>
  )
}
