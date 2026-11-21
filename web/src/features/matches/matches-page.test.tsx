import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { makeQueryClient } from '@/lib/query'
import { fail, installApi, ok, renderApp, signedIn, type Handler, type Req } from '@/test/fake-api'
import { match, occurrence } from './fixtures'
import type { PaymentMatch } from './types'

let calls: Req[]
let store: PaymentMatch[]
let extra: Record<string, Handler>

const routes = (): Record<string, Handler> => ({
  ...signedIn(),
  'GET /api/v1/payment-matches': (r) => ok({ items: store.filter((m) => m.state === r.query.get('state')), next_cursor: null }),
  'GET /api/v1/occurrences/occ-a': ok(occurrence('a')),
  'GET /api/v1/commitments/c-a': ok({ id: 'c-a', name: 'Rent' }),
  ...extra,
})
const start = (path = '/matches') => {
  calls = installApi(routes())
  return renderApp(path, makeQueryClient())
}
const posts = () => calls.filter((c) => c.method === 'POST')

beforeEach(() => {
  store = [match('a')]
  extra = {}
})
afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

describe('the review queue', () => {
  it('lists a suggestion with the bank fact, the origin and the hint warning, and never calls a score proof', async () => {
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    expect(card).toHaveTextContent('Landlord Ltd · RENT NOVEMBER')
    expect(card).toHaveTextContent('Suggested by a matching rule')
    expect(card).toHaveTextContent('a hint, never proof')
    expect(card).toHaveTextContent('Suggestion')
    expect(card).not.toHaveTextContent(/paid by this transaction/i)
    expect(screen.getByText(/Only confirmed matches count as payment/)).toBeInTheDocument()
  })

  it('hides bank text without banking:read and says why', async () => {
    store = [match('a', { bank_text_redacted: true, transaction: { ...match('a').transaction, description: null, counterparty: null } })]
    start()
    expect(await screen.findByText(/needs the banking:read grant/)).toBeInTheDocument()
    expect(screen.queryByTestId('bank-text')).not.toBeInTheDocument()
  })

  it('does not offer to confirm a pending transaction and says when it can be', async () => {
    store = [match('a', { transaction: { ...match('a').transaction, booking_status: 'pending', countable: false } })]
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    expect(within(card).getByRole('button', { name: 'Confirm match' })).toBeDisabled()
    expect(card).toHaveTextContent('Only a booked transaction can be confirmed')
    expect(within(card).getByRole('button', { name: 'Reject suggestion' })).toBeEnabled()
  })

  it('says nothing is waiting when the queue is empty', async () => {
    store = []
    start()
    expect(await screen.findByText(/No suggestions are waiting/)).toBeInTheDocument()
  })
})

describe('confirming: nothing is shown as saved before the server confirms it', () => {
  it('shows "Not saved yet" and keeps the suggestion a suggestion while the request is out, then reports it saved with an Undo', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    extra['POST /api/v1/payment-matches/a/confirm'] = async () => {
      await gate
      store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true })]
      return ok(store[0])
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    expect(await within(card).findAllByText('Not saved yet')).not.toHaveLength(0)
    expect(card).toHaveTextContent('still suggested')
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Confirming…' })).toBeDisabled()
    // The page holds no "confirmed" card yet.
    expect(card).toHaveAttribute('data-state', 'suggested')
    await act(async () => release())
    expect(await screen.findByText(/Match confirmed/)).toBeInTheDocument()
    expect(screen.getByText(/It now counts as payment/)).toBeInTheDocument()
    expect(posts()[0]!.headers['If-Match']).toBe('"1"')
    expect(posts()[0]!.body).toEqual({ version: 1 })
    // The suggestion left the queue once the server had answered.
    await waitFor(() => expect(screen.queryByRole('article', { name: 'Rent' })).not.toBeInTheDocument())
  })

  it('Undo of a confirmation is its own save, confirmed by the server, and the notice stays until dismissed', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true })]
      return ok(store[0])
    }
    extra['POST /api/v1/payment-matches/a/unmatch'] = () => {
      store = [match('a', { state: 'rejected', version: 3 })]
      return ok(store[0])
    }
    start()
    await userEvent.click(within(await screen.findByRole('article', { name: 'Rent' })).getByRole('button', { name: 'Confirm match' }))
    await userEvent.click(await screen.findByRole('button', { name: 'Undo this match' }))
    expect(await screen.findByText('Undone')).toBeInTheDocument()
    expect(posts()[1]!.headers['If-Match']).toBe('"2"')
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('Undone')).not.toBeInTheDocument()
  })

  it('a failed save says it was not saved, keeps the suggestion as it was, and can be tried again', async () => {
    let n = 0
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      n++
      if (n === 1) return fail(500, 'internal', 'The server had a problem.')
      store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true })]
      return ok(store[0])
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    const alert = await within(card).findByRole('alert')
    expect(alert).toHaveTextContent('Not saved')
    expect(alert).toHaveTextContent('Nothing was saved; the match is unchanged')
    expect(card).toHaveAttribute('data-state', 'suggested')
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
    await userEvent.click(within(alert).getByRole('button', { name: 'Try again' }))
    expect(await screen.findByText(/Match confirmed/)).toBeInTheDocument()
    expect(n).toBe(2)
  })

  it('a network failure is a failed save too, never a saved one', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      throw new TypeError('network down')
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    expect(await within(card).findByRole('alert')).toHaveTextContent('Not saved')
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
  })

  it('offers an explicit overpayment choice instead of allowing it silently', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = (r) => {
      if ((r.body as { allow_overpayment?: boolean }).allow_overpayment) {
        store = [match('a', { state: 'confirmed', version: 2 })]
        return ok(store[0])
      }
      return fail(409, 'occurrence_overpaid', 'The occurrence would be paid more than its amount.', { field: 'amount' })
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    expect(await within(card).findByRole('alert')).toHaveTextContent('This would pay more than the amount due')
    expect(posts()).toHaveLength(1)
    await userEvent.click(within(card).getByRole('button', { name: 'Allow overpayment and try again' }))
    await screen.findByText(/Match confirmed/)
    expect(posts()[1]!.body).toEqual({ version: 1, allow_overpayment: true })
  })
})

describe('a conflict (409) is reconciled, not overwritten', () => {
  const conflict = (current: PaymentMatch) => () => {
    store = [current]
    return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: current.version, current } })
  }

  it('loads the latest version and resends the same change on it only after the person chooses to', async () => {
    let n = 0
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      n++
      if (n === 1) return conflict(match('a', { version: 3, reason: 'edited elsewhere' }))()
      store = [match('a', { state: 'confirmed', version: 4 })]
      return ok(store[0])
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    const alert = await within(card).findByRole('alert')
    expect(alert).toHaveTextContent('Not saved: changed elsewhere')
    expect(alert).toHaveTextContent('The latest version is shown above')
    // The card now shows the server's latest data, and nothing was resent behind the person's back.
    await waitFor(() => expect(card).toHaveTextContent('edited elsewhere'))
    expect(posts()).toHaveLength(1)
    await userEvent.click(within(alert).getByRole('button', { name: 'Save again on the latest version' }))
    await screen.findByText(/Match confirmed/)
    expect(posts()[1]!.headers['If-Match']).toBe('"3"')
  })

  it('offers no resend when the match no longer allows the change (it was rejected elsewhere)', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = conflict(match('a', { state: 'rejected', version: 2 }))
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    const alert = await within(card).findByRole('alert')
    expect(alert).toHaveTextContent('It no longer allows this change')
    expect(within(alert).queryByRole('button', { name: /Save again/ })).not.toBeInTheDocument()
    // The refetch no longer lists it as a suggestion, but the failure stays visible until dismissed.
    await waitFor(() => expect(card).toHaveTextContent('No longer in this list'))
    expect(screen.getByRole('article', { name: 'Rent' })).toBeInTheDocument()
    await userEvent.click(within(alert).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('article', { name: 'Rent' })).not.toBeInTheDocument())
    expect(posts()).toHaveLength(1)
  })
})

describe('correcting a confirmed match, by keyboard alone', () => {
  it('opens the Confirmed list, corrects the amount and unmatches without a pointer', async () => {
    store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true, amount: { amount: '10.00', currency: 'EUR' } })]
    extra['POST /api/v1/payment-matches/a/correct'] = (r) => {
      const amount = (r.body as { amount: { amount: string } }).amount.amount
      store = [match('a', { state: 'confirmed', version: 3, counts_as_evidence: true, amount: { amount, currency: 'EUR' } })]
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    // Reach "Confirmed" with Tab and Enter.
    const confirmedTab = await screen.findByRole('button', { name: 'Confirmed' })
    while (document.activeElement !== confirmedTab) await user.tab()
    await user.keyboard('{Enter}')
    const card = await screen.findByRole('article', { name: 'Rent' })
    const correct = within(card).getByRole('button', { name: 'Correct amount' })
    while (document.activeElement !== correct) await user.tab()
    await user.keyboard('{Enter}')
    // Focus lands in the amount field of the form that opened.
    const amount = within(card).getByLabelText(/Allocated amount/)
    expect(amount).toHaveFocus()
    await user.keyboard('{Control>}a{/Control}7.50')
    await user.tab() // reason
    await user.keyboard('bank fee')
    await user.tab() // save
    expect(within(card).getByRole('button', { name: 'Save correction' })).toHaveFocus()
    await user.keyboard('{Enter}')
    // The result is announced in a notice that takes focus.
    await screen.findByText(/Amount corrected/)
    expect(posts()[0]!.body).toEqual({ version: 2, reason: 'bank fee', amount: { amount: '7.50', currency: 'EUR' } })
    expect(posts()[0]!.headers['If-Match']).toBe('"2"')
    await waitFor(() => expect(screen.getByText(/Amount corrected/).closest('[tabindex="-1"]')).toHaveFocus())
  })

  it('Escape-free cancel returns focus to the button that opened the form', async () => {
    store = [match('a', { state: 'confirmed', version: 2 })]
    start('/matches')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Confirmed' }))
    const card = await screen.findByRole('article', { name: 'Rent' })
    const unmatch = within(card).getByRole('button', { name: /Unmatch/ })
    unmatch.focus()
    await user.keyboard('{Enter}')
    expect(within(card).getByLabelText(/Reason/)).toHaveFocus()
    await user.click(within(card).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(within(card).getByRole('button', { name: /Unmatch/ })).toHaveFocus())
    expect(posts()).toHaveLength(0)
  })

  it('says a confirmed match whose transaction is no longer reported does not count', async () => {
    store = [match('a', { state: 'confirmed', needs_review: true, counts_as_evidence: false })]
    start()
    await userEvent.click(await screen.findByRole('button', { name: 'Confirmed' }))
    const card = await screen.findByRole('article', { name: 'Rent' })
    expect(card).toHaveTextContent('Confirmed, not counting')
    expect(card).toHaveTextContent('does not count as payment until it is reported again')
  })
})

describe('run matching', () => {
  it('reports the counts only after the server answered, and a failure says nothing changed', async () => {
    let n = 0
    extra['POST /api/v1/payment-matching/runs'] = () => {
      n++
      return n === 1 ? fail(500, 'internal', 'The engine failed.') : ok({ candidates: 3, confirmed: 1, suggested: 2, dropped: 0, skipped: 0 })
    }
    start()
    await userEvent.click(await screen.findByRole('button', { name: 'Run matching now' }))
    expect(await screen.findByText('Matching did not run')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByTestId('run-result')).toHaveTextContent('1 confirmed automatically, 2 new suggestions')
  })
})
