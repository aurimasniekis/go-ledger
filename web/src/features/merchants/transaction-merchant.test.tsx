import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, fail, installApi, ok, signedIn } from '@/test/fake-api'
import { MerchantDisclosure, MerchantPanel, confidenceText } from './transaction-merchant'
import { merchant, txMerchant } from './fixtures'
import type { Enrichment, MerchantSource } from './types'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const enrichment = (over: Partial<Enrichment> = {}): Enrichment => ({
  status: 'identified', name: 'Lidl', logo_ref: 'https://logos.example/lidl.png', source: 'curated', confidence_permille: 940, candidates: [], retrieved_at: '2026-11-21T10:00:00Z', last_failed_at: null, merchant_id: null, ...over,
})
const SOURCES: MerchantSource[] = [
  { name: 'curated', kind: 'local', enabled: true, description: 'A built-in list', sends_off_machine: [], attribution: null, stores_logo_ref: false, retention_hours: null },
  { name: 'remote-x', kind: 'remote', enabled: true, description: 'A remote lookup', sends_off_machine: ['counterparty name'], attribution: 'Merchant data by Example Logos', stores_logo_ref: false, retention_hours: 720 },
]
const url = '/api/v1/transactions/tx-1/merchant'

function panel(routes: Parameters<typeof installApi>[0], grants = ALL_GRANTS) {
  const calls = installApi({ ...signedIn(undefined, grants), 'GET /api/v1/merchants/sources': ok({ sources: SOURCES }), 'GET /api/v1/merchants': ok({ items: [merchant('L00', 'Lidl')], next_cursor: null }), ...routes })
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MerchantPanel transactionId="tx-1" canWrite={grants.includes('merchants:write')} />
    </QueryClientProvider>,
  )
  return calls
}

describe('confidenceText', () => {
  it('writes a per-mille integer as a percentage without floating point', () => {
    expect(confidenceText(940)).toBe('94.0%')
    expect(confidenceText(905)).toBe('90.5%')
    expect(confidenceText(0)).toBe('0.0%')
    expect(confidenceText(1000)).toBe('100.0%')
  })
})

describe('transaction merchant', () => {
  it('shows a reliable name with its source and the attribution the source asks for, and never loads the logo', async () => {
    panel({ ['GET ' + url]: ok(txMerchant({ state: 'reliable', fallback: false, name: 'Lidl', logo_ref: 'https://logos.example/lidl.png', identity_source: 'remote-x', enrichment: enrichment({ source: 'remote-x' }) })) })
    expect(await screen.findByTestId('merchant-name')).toHaveTextContent('Lidl')
    expect(screen.getByText(/Identified by remote-x, confidence 94\.0%/)).toBeInTheDocument()
    expect(await screen.findByTestId('attribution')).toHaveTextContent('Merchant data by Example Logos')
    expect(document.querySelector('img')).toBeNull()
  })

  it('shows no name, and no attribution, when the identification is uncertain, and offers the suggestion only as a suggestion', async () => {
    panel({ ['GET ' + url]: ok(txMerchant({ state: 'uncertain', needs_review: true, enrichment: enrichment({ source: 'remote-x', confidence_permille: 400 }) })) })
    expect(await screen.findByTestId('merchant-name')).toHaveTextContent('No merchant name shown')
    expect(screen.getByText(/low confidence, so no name is shown until you confirm or correct it/)).toBeInTheDocument()
    expect(screen.getByTestId('suggestion')).toHaveTextContent('not used until you confirm it: Lidl, confidence 40.0%')
    expect(screen.queryByTestId('attribution')).toBeNull()
  })

  it.each([
    ['ambiguous', /found several possible merchants/],
    ['none', /No source knows a merchant/],
    ['unavailable', /lookup failed/],
    ['not_looked_up', /No lookup has been made/],
    ['rejected', /said this transaction has no merchant/],
  ] as const)('shows the neutral presentation and the reason for %s', async (state, why) => {
    panel({ ['GET ' + url]: ok(txMerchant({ state })) })
    expect(await screen.findByTestId('merchant-name')).toHaveTextContent('No merchant name shown')
    expect(screen.getByText(why)).toBeInTheDocument()
  })

  it("shows a corrected name as the person's own, without attribution", async () => {
    panel({ ['GET ' + url]: ok(txMerchant({ state: 'corrected', fallback: false, name: 'Corner shop', identity_source: 'user', enrichment: enrichment({ source: 'remote-x' }) })) })
    expect(await screen.findByTestId('merchant-name')).toHaveTextContent('Corner shop')
    expect(screen.getByText('You gave this name.')).toBeInTheDocument()
    expect(screen.queryByTestId('attribution')).toBeNull()
  })

  it('warns when a confirmation is outdated and offers to confirm the new answer', async () => {
    const user = userEvent.setup()
    let current = txMerchant({ state: 'reliable', fallback: false, name: 'Lidl', confirmation_outdated: true, needs_review: true, enrichment: enrichment() })
    const calls = panel({
      ['GET ' + url]: () => ok(current),
      ['PUT ' + url + '/review']: () => ok((current = txMerchant({ state: 'confirmed', fallback: false, name: 'Lidl', enrichment: enrichment() }))),
    })
    expect(await screen.findByText(/no longer applies/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Confirm this merchant' }))
    expect(await screen.findByText('Confirmed.')).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ decision: 'confirmed' })
    expect(screen.getByText('You confirmed the name the source found.')).toBeInTheDocument()
  })

  it('corrects by name, creating or using the merchant record, and only says so once saved', async () => {
    const user = userEvent.setup()
    let current = txMerchant()
    const calls = panel({
      ['GET ' + url]: () => ok(current),
      ['PUT ' + url + '/review']: () => ok((current = txMerchant({ state: 'corrected', fallback: false, name: 'Corner shop', identity_source: 'user' }))),
    })
    await user.click(await screen.findByRole('button', { name: 'Correct the merchant' }))
    await user.type(screen.getByLabelText('Merchant name'), 'Corner shop{Enter}')
    expect(await screen.findByText('Corrected.')).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ decision: 'corrected', name: 'Corner shop' })
    expect(screen.getByTestId('merchant-name')).toHaveTextContent('Corner shop')
  })

  it('corrects by choosing an existing merchant and sends its identifier only', async () => {
    const user = userEvent.setup()
    const calls = panel({ ['GET ' + url]: ok(txMerchant()), ['PUT ' + url + '/review']: ok(txMerchant({ state: 'corrected', fallback: false, name: 'Lidl' })) })
    await user.click(await screen.findByRole('button', { name: 'Correct the merchant' }))
    await user.selectOptions(await screen.findByLabelText('Or an existing merchant'), merchant('L00', 'Lidl').id)
    await user.click(screen.getByRole('button', { name: 'Save correction' }))
    await screen.findByText('Corrected.')
    expect(calls.find((c) => c.method === 'PUT')!.body).toEqual({ decision: 'corrected', merchant_id: merchant('L00', 'Lidl').id })
  })

  it('refuses an empty correction locally and shows a refused archived merchant', async () => {
    const user = userEvent.setup()
    const calls = panel({ ['GET ' + url]: ok(txMerchant()), ['PUT ' + url + '/review']: fail(409, 'merchant_archived', 'archived') })
    await user.click(await screen.findByRole('button', { name: 'Correct the merchant' }))
    await user.click(screen.getByRole('button', { name: 'Save correction' }))
    expect(screen.getByText(/Enter the merchant name, or choose an existing merchant/)).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PUT')).toBe(false)
    await user.type(screen.getByLabelText('Merchant name'), 'Old{Enter}')
    expect(await screen.findByText(/archived or merged.*Nothing was changed/)).toBeInTheDocument()
    expect(screen.queryByText('Corrected.')).toBeNull()
  })

  it('rejects a merchant, and explains nothing_to_confirm when there is nothing to confirm', async () => {
    const user = userEvent.setup()
    const calls = panel({
      ['GET ' + url]: ok(txMerchant({ state: 'uncertain', enrichment: enrichment({ confidence_permille: 300 }) })),
      ['PUT ' + url + '/review']: (req) => ((req.body as { decision: string }).decision === 'confirmed' ? fail(409, 'nothing_to_confirm', 'nothing') : ok(txMerchant({ state: 'rejected' }))),
    })
    await user.click(await screen.findByRole('button', { name: 'Confirm this merchant' }))
    expect(await screen.findByText(/did not identify a merchant.*Nothing was changed/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'This is not a merchant' }))
    expect(await screen.findByText('Marked as having no merchant.')).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PUT').map((c) => c.body)).toEqual([{ decision: 'confirmed' }, { decision: 'rejected' }])
  })

  it('takes a decision back, but sends the person to the pending transaction when it is inherited', async () => {
    const user = userEvent.setup()
    const review = { transaction_id: 'tx-1', decision: 'rejected', name: null, merchant_id: null, by: { kind: 'user', user_id: 'u', via: 'session' }, decided_at: '2026-11-21T10:00:00Z', version: 1 } as const
    const calls = panel({ ['GET ' + url]: ok(txMerchant({ state: 'rejected', review })), ['DELETE ' + url + '/review']: ok(txMerchant()) })
    await user.click(await screen.findByRole('button', { name: 'Take my decision back' }))
    expect(await screen.findByText('Your decision was taken back.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(true)
  })

  it('does not offer to clear an inherited decision, and names the transaction that holds it', async () => {
    const review = { transaction_id: 'tx-pending', decision: 'corrected', name: 'Lidl', merchant_id: null, by: { kind: 'user', user_id: 'u', via: 'session' }, decided_at: '2026-11-21T10:00:00Z', version: 1 } as const
    panel({ ['GET ' + url]: ok(txMerchant({ state: 'corrected', fallback: false, name: 'Lidl', review_inherited: true, review })) })
    expect(await screen.findByText(/inherited from the pending transaction/)).toHaveTextContent('tx-pending')
    expect(screen.queryByRole('button', { name: 'Take my decision back' })).toBeNull()
  })

  it('offers no change without merchants:write', async () => {
    panel({ ['GET ' + url]: ok(txMerchant()) }, ['merchants:read'])
    await screen.findByTestId('merchant-name')
    expect(screen.queryByRole('button', { name: 'Correct the merchant' })).toBeNull()
    expect(screen.getByText(/does not hold merchants:write/)).toBeInTheDocument()
  })

  it('is not offered without merchants:read', async () => {
    const calls = installApi({ ...signedIn(undefined, ['commitments:read']) })
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MerchantDisclosure transactionId="tx-1" subject="x" />
      </QueryClientProvider>,
    )
    await vi.waitFor(() => expect(calls.some((c) => c.path === '/api/v1/me')).toBe(true))
    expect(screen.queryByRole('button', { name: /Merchant of/ })).toBeNull()
    expect(calls.some((c) => c.path === url)).toBe(false)
  })

  it('reads nothing until it is opened, and opens as a disclosure', async () => {
    const user = userEvent.setup()
    const calls = installApi({ ...signedIn(), ['GET ' + url]: ok(txMerchant()), 'GET /api/v1/merchants/sources': ok({ sources: [] }) })
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MerchantDisclosure transactionId="tx-1" subject="the 5.00 EUR bank transaction" />
      </QueryClientProvider>,
    )
    const button = await screen.findByRole('button', { name: 'Merchant of the 5.00 EUR bank transaction' })
    expect(button).toHaveAttribute('aria-expanded', 'false')
    expect(calls.some((c) => c.path === url)).toBe(false)
    await user.click(button)
    expect(button).toHaveAttribute('aria-expanded', 'true')
    expect(await screen.findByTestId('merchant-name')).toBeInTheDocument()
  })
})
