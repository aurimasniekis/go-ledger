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
    // The first findBy waits for the session, /me, the list, the occurrence and the commitment: more than the default 1 s on a loaded CI machine.
    const card = await screen.findByRole('article', { name: 'Rent' }, { timeout: 5000 })
    expect(card).toHaveTextContent('Landlord Ltd · RENT NOVEMBER')
    expect(card).toHaveTextContent('Found by a matching rule')
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
    expect(card).toHaveTextContent('still a suggestion')
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Confirming…' })).toHaveAttribute('aria-disabled', 'true')
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

  it('the unmatch after a confirmation is its own save, confirmed by the server, and the notice stays until dismissed', async () => {
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
    await userEvent.click(await screen.findByRole('button', { name: 'Unmatch (final)' }))
    expect(await screen.findByText('Unmatched')).toBeInTheDocument()
    expect(posts()[1]!.headers['If-Match']).toBe('"2"')
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('Unmatched')).not.toBeInTheDocument()
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
    expect(alert).toHaveTextContent('It no longer allows any change')
    expect(within(alert).queryByRole('button', { name: /Save again/ })).not.toBeInTheDocument()
    // The refetch no longer lists it as a suggestion, but the failure stays visible until dismissed.
    await waitFor(() => expect(card).toHaveTextContent('Changed elsewhere: now rejected'))
    expect(screen.getByRole('article', { name: 'Rent' })).toBeInTheDocument()
    await userEvent.click(within(alert).getByRole('button', { name: 'Dismiss' }))
    await waitFor(() => expect(screen.queryByRole('article', { name: 'Rent' })).not.toBeInTheDocument())
    expect(posts()).toHaveLength(1)
  })
})

describe('correcting a confirmed match, by keyboard alone', () => {
  it('opens the Confirmed list and corrects the amount without a pointer', async () => {
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

  it('Cancel by keyboard returns focus to the button that opened the form', async () => {
    store = [match('a', { state: 'confirmed', version: 2 })]
    start('/matches')
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: 'Confirmed' }))
    const card = await screen.findByRole('article', { name: 'Rent' })
    const unmatch = within(card).getByRole('button', { name: /Unmatch/ })
    unmatch.focus()
    await user.keyboard('{Enter}')
    expect(within(card).getByLabelText(/Reason/)).toHaveFocus()
    await user.tab() // reason -> Unmatch now
    await user.tab() // -> Cancel
    expect(within(card).getByRole('button', { name: 'Cancel' })).toHaveFocus()
    await user.keyboard('{Enter}')
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

const confirmedStore = (over: Partial<PaymentMatch> = {}) => {
  store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true, ...over })]
}
const openCorrection = async (user: ReturnType<typeof userEvent.setup>) => {
  const confirmedTab = await screen.findByRole('button', { name: 'Confirmed' })
  while (document.activeElement !== confirmedTab) await user.tab()
  await user.keyboard('{Enter}')
  const card = await screen.findByRole('article', { name: 'Rent' })
  const correct = within(card).getByRole('button', { name: 'Correct amount' })
  while (document.activeElement !== correct) await user.tab()
  await user.keyboard('{Enter}')
  await user.keyboard('{Control>}a{/Control}7.50')
  return card
}

describe('a correction by keyboard when the save does not go through', () => {
  it('a failed save keeps the typed amount, says nothing was saved, and Try again resends exactly it', async () => {
    confirmedStore()
    let n = 0
    extra['POST /api/v1/payment-matches/a/correct'] = (r) => {
      n++
      if (n === 1) return fail(500, 'internal', 'The server had a problem.')
      const amount = (r.body as { amount: { amount: string } }).amount.amount
      confirmedStore({ version: 3, amount: { amount, currency: 'EUR' } })
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    const card = await openCorrection(user)
    await user.tab() // reason
    await user.tab() // save
    await user.keyboard('{Enter}')
    const alert = await within(card).findByRole('alert')
    expect(alert).toHaveTextContent('Not saved')
    expect(alert).toHaveTextContent('Nothing was saved; the match is unchanged')
    expect(within(card).getByLabelText(/Allocated amount/)).toHaveValue('7.50')
    expect(screen.queryByText('Saved')).not.toBeInTheDocument()
    expect(card).toHaveTextContent('10.00')
    const retry = within(alert).getByRole('button', { name: 'Try again' })
    while (document.activeElement !== retry) await user.tab()
    await user.keyboard('{Enter}')
    await screen.findByText(/Amount corrected/)
    expect(posts()).toHaveLength(2)
    expect(posts()[1]!.body).toEqual(posts()[0]!.body)
    expect(posts()[1]!.body).toEqual({ version: 2, amount: { amount: '7.50', currency: 'EUR' } })
  })

  it('a 409 shows the latest match, resends the typed amount on its version only when the person chooses, and never on its own', async () => {
    confirmedStore()
    let n = 0
    extra['POST /api/v1/payment-matches/a/correct'] = () => {
      n++
      if (n === 1) {
        const current = match('a', { state: 'confirmed', version: 4, counts_as_evidence: true, reason: 'edited elsewhere' })
        store = [current]
        return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 4, current } })
      }
      confirmedStore({ version: 5, amount: { amount: '7.50', currency: 'EUR' } })
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    const card = await openCorrection(user)
    await user.tab()
    await user.tab()
    await user.keyboard('{Enter}')
    const alert = await within(card).findByRole('alert')
    expect(alert).toHaveTextContent('Not saved: changed elsewhere')
    expect(card).toHaveTextContent('edited elsewhere')
    expect(within(card).getByLabelText(/Allocated amount/)).toHaveValue('7.50')
    expect(posts()).toHaveLength(1)
    const again = within(alert).getByRole('button', { name: 'Save again on the latest version' })
    while (document.activeElement !== again) await user.tab()
    await user.keyboard('{Enter}')
    await screen.findByText(/Amount corrected/)
    expect(posts()).toHaveLength(2)
    expect(posts()[1]!.headers['If-Match']).toBe('"4"')
    expect(posts()[1]!.body).toEqual({ version: 4, amount: { amount: '7.50', currency: 'EUR' } })
  })

  it('an amount that is not a decimal is stopped before any request, marked invalid, and fixed by typing', async () => {
    confirmedStore()
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('button', { name: 'Confirmed' }).then(async (b) => {
      await user.click(b)
      return screen.findByRole('article', { name: 'Rent' })
    })
    await user.click(within(card).getByRole('button', { name: 'Correct amount' }))
    const input = within(card).getByLabelText(/Allocated amount/)
    await user.clear(input)
    await user.type(input, 'abc')
    await user.click(within(card).getByRole('button', { name: 'Save correction' }))
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(input).toHaveAccessibleDescription('Enter an amount such as 12.50, more than zero.')
    expect(input).toHaveFocus()
    expect(posts()).toHaveLength(0)
    await user.type(input, '{Backspace}{Backspace}{Backspace}7.5')
    expect(input).not.toHaveAttribute('aria-invalid')
  })

  it('a 422 from the server marks the amount invalid, says it once, and offers no Try again', async () => {
    confirmedStore()
    extra['POST /api/v1/payment-matches/a/correct'] = () =>
      fail(422, 'validation_failed', 'amount.amount must be a decimal string such as "12.50"', { field: 'amount.amount' })
    start()
    const user = userEvent.setup()
    const card = await openCorrection(user)
    await user.tab()
    await user.tab()
    await user.keyboard('{Enter}')
    const alert = await within(card).findByRole('alert')
    const input = within(card).getByLabelText(/Allocated amount/)
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(alert).toHaveTextContent('Fix the amount above and save again')
    expect(alert).not.toHaveTextContent('decimal string')
    expect(within(alert).queryByRole('button', { name: 'Try again' })).not.toBeInTheDocument()
    expect(card).toHaveTextContent('Enter an amount such as 12.50')
    await user.type(input, '0')
    await waitFor(() => expect(within(card).queryByRole('alert')).not.toBeInTheDocument())
  })
})

describe('reasons and unsaved text do not leak', () => {
  it('a reason typed into a cancelled form is not sent with the next one-step Confirm', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      confirmedStore()
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await user.click(within(card).getByRole('button', { name: 'Reject suggestion' }))
    await user.type(within(card).getByLabelText(/Reason/), 'not mine')
    await user.click(within(card).getByRole('button', { name: 'Cancel' }))
    await user.click(within(card).getByRole('button', { name: 'Confirm match' }))
    await screen.findByText(/Match confirmed/)
    expect(posts()[0]!.body).toEqual({ version: 1 })
  })

  it('Escape closes the form, and a reopened form starts with no reason', async () => {
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await user.click(within(card).getByRole('button', { name: 'Reject suggestion' }))
    await user.type(within(card).getByLabelText(/Reason/), 'later')
    await user.keyboard('{Escape}')
    expect(within(card).queryByRole('form')).not.toBeInTheDocument()
    await user.click(within(card).getByRole('button', { name: 'Reject suggestion' }))
    expect(within(card).getByLabelText(/Reason/)).toHaveValue('')
  })
})

describe('a conflict while a form is open (R1)', () => {
  it('replaces the submit with a pointer to "Save again on the latest version" and moves focus to the alert', async () => {
    let n = 0
    extra['POST /api/v1/payment-matches/a/correct'] = () => {
      const current = match('a', { state: 'confirmed', version: 4, counts_as_evidence: true })
      store = [current]
      if (++n > 1) return ok(current)
      return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 4, current } })
    }
    confirmedStore()
    start()
    const user = userEvent.setup()
    const card = await openCorrection(user)
    await user.tab()
    await user.tab()
    await user.keyboard('{Enter}')
    const alert = await within(card).findByRole('alert')
    const form = within(card).getByRole('form', { name: 'Correct amount' })
    expect(within(form).queryByRole('button', { name: 'Save correction' })).not.toBeInTheDocument()
    expect(form).toHaveTextContent('Save again on the latest version')
    expect(alert).toHaveFocus()
    await user.click(within(alert).getByRole('button', { name: 'Save again on the latest version' }))
    await screen.findByText(/Amount corrected/)
    expect(posts()).toHaveLength(2)
  })

  it('closes the form when the latest copy no longer allows the change, so nothing can be sent', async () => {
    extra['POST /api/v1/payment-matches/a/reject'] = () => {
      const current = match('a', { state: 'rejected', version: 2 })
      store = [current]
      return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 2, current } })
    }
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await user.click(within(card).getByRole('button', { name: 'Reject suggestion' }))
    await user.type(within(card).getByLabelText(/Reason/), 'not it')
    await user.click(within(card).getByRole('button', { name: 'Reject now' }))
    const alert = await within(card).findByRole('alert')
    expect(within(card).queryByRole('form')).not.toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: 'Reject now' })).not.toBeInTheDocument()
    expect(alert).toHaveFocus()
    expect(posts()).toHaveLength(1)
  })
})

describe('a competing suggestion after the other one was confirmed (R2)', () => {
  it('shows it as already paid, with Reject first, and never offers Confirm as the primary action', async () => {
    let paid = false
    store = [match('a'), match('b', { occurrence_id: 'occ-a' })]
    extra['GET /api/v1/occurrences/occ-a'] = () => ok({ ...occurrence('a'), payment: paid ? { status: 'paid' } : { status: 'unpaid' } })
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      paid = true
      store = [match('a', { state: 'confirmed', version: 2, counts_as_evidence: true }), match('b', { occurrence_id: 'occ-a' })]
      return ok(store[0])
    }
    start()
    const cards = await screen.findAllByRole('article', { name: 'Rent' })
    expect(cards).toHaveLength(2)
    expect(within(cards[1]!).getByTestId('competing')).toBeInTheDocument()
    await userEvent.click(within(cards[0]!).getByRole('button', { name: 'Confirm match' }))
    await screen.findByText(/Match confirmed/)
    const other = await waitFor(() => {
      const rest = screen.getAllByRole('article', { name: 'Rent' })
      expect(rest).toHaveLength(1)
      expect(within(rest[0]!).getByTestId('already-paid')).toBeInTheDocument()
      return rest[0]!
    })
    expect(other).toHaveAttribute('data-match-id', 'b')
    expect(within(other).queryByTestId('competing')).not.toBeInTheDocument()
    expect(other).toHaveTextContent('may be out of date')
    expect(within(other).getByRole('button', { name: 'Reject suggestion' }).className).toContain('bg-primary')
    expect(within(other).getByRole('button', { name: 'Confirm match' }).className).not.toContain('bg-primary')
  })
})

describe('"Already paid" says where the existing payment comes from (N1)', () => {
  const pay = (over: Record<string, unknown>) => ({ status: 'paid', bank_evidence_count: 0, manual_evidence_count: 0, override: null, ...over })
  const cardWith = async (payment: Record<string, unknown>, matchOver: Record<string, unknown> = {}) => {
    store = [match('a', matchOver)]
    extra['GET /api/v1/occurrences/occ-a'] = () => ok({ ...occurrence('a'), payment })
    start()
    const card = await screen.findByRole('article', { name: 'Rent' }, { timeout: 5000 })
    const note = await within(card).findByTestId('already-paid')
    return { card, note }
  }
  const order = (card: HTMLElement) => within(card).getAllByRole('button').map((b) => b.textContent)

  it('a manual record: does not call it another transaction, does not push Reject as the primary action', async () => {
    const { card, note } = await cardWith(pay({ manual_evidence_count: 1 }))
    expect(note).toHaveTextContent('You recorded this occurrence as paid by hand')
    expect(note).not.toHaveTextContent(/Another (bank )?transaction/)
    expect(note).toHaveTextContent('cannot link')
    expect(within(card).getByRole('button', { name: 'Reject suggestion' }).className).not.toContain('bg-primary')
    expect(within(card).getByRole('button', { name: 'Confirm match' }).className).not.toContain('bg-primary')
    expect(order(card)[0]).toBe('Reject suggestion')
  })

  it('a bank match: names the other bank transaction, with Reject first and primary', async () => {
    const { card, note } = await cardWith(pay({ bank_evidence_count: 1 }))
    expect(note).toHaveTextContent('Another bank transaction already pays')
    expect(within(card).getByRole('button', { name: 'Reject suggestion' }).className).toContain('bg-primary')
    expect(order(card)[0]).toBe('Reject suggestion')
  })

  it('bank and manual together: the bank wording', async () => {
    const { note } = await cardWith(pay({ bank_evidence_count: 1, manual_evidence_count: 1 }))
    expect(note).toHaveTextContent('Another bank transaction already pays')
  })

  it('a paid override over no evidence: says it was marked by hand', async () => {
    const { card, note } = await cardWith(pay({ override: { status: 'paid', reason: 'cash', set_at: '2026-11-20T00:00:00Z', applied: true } }))
    expect(note).toHaveTextContent('marked paid by hand')
    expect(note).not.toHaveTextContent(/Another (bank )?transaction/)
    expect(within(card).getByRole('button', { name: 'Reject suggestion' }).className).not.toContain('bg-primary')
  })

  it('drops the stale "a person has to choose" lines by engine code, and keeps the other sentences', async () => {
    const reason = 'automatic match not confirmed: several_transactions (2 candidate transactions for this occurrence); date_outside_window (9 days from the due date; at most 3); description similarity 80/100 (a hint only)'
    const { card } = await cardWith(pay({ bank_evidence_count: 1 }), { reason })
    expect(card).not.toHaveTextContent('a person has to choose')
    expect(card).toHaveTextContent('It is 9 days from the due date; automatic confirmation allows at most 3.')
  })
})

describe('a card after a real conflict shows the latest state, with only valid actions', () => {
  it('shows the rejection from elsewhere as the state, with no actions left', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      const current = match('a', { state: 'rejected', version: 2, reason: 'rejected in another tab', decided_by: { kind: 'user', user_id: null } })
      store = [current]
      return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 2, current } })
    }
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    await within(card).findByRole('alert')
    expect(card).toHaveAttribute('data-state', 'rejected')
    expect(card).toHaveTextContent('Changed elsewhere: now rejected')
    expect(card).toHaveTextContent('rejected in another tab')
    expect(card).not.toHaveTextContent(/^.*Suggestion\b/)
    for (const name of ['Confirm match', 'Reject suggestion', 'Correct amount']) expect(within(card).queryByRole('button', { name })).not.toBeInTheDocument()
  })

  it('shows a match confirmed elsewhere with the actions a confirmed match allows, not the stale ones', async () => {
    extra['POST /api/v1/payment-matches/a/reject'] = () => {
      const current = match('a', { state: 'confirmed', version: 2, counts_as_evidence: true })
      store = [current]
      return fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 2, current } })
    }
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await user.click(within(card).getByRole('button', { name: 'Reject suggestion' }))
    await user.click(within(card).getByRole('button', { name: 'Reject now' }))
    await within(card).findByRole('alert')
    expect(card).toHaveAttribute('data-state', 'confirmed')
    expect(within(card).queryByRole('button', { name: 'Confirm match' })).not.toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'Unmatch (final)' })).toBeInTheDocument()
  })

  it('says the card is out of date, and offers no action, when the conflict carries no latest copy', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => fail(409, 'version_conflict', 'The match changed since you read it.', { reconciliation: { current_version: 7 } })
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    await userEvent.click(within(card).getByRole('button', { name: 'Confirm match' }))
    await within(card).findByRole('alert')
    expect(card).toHaveTextContent('Out of date')
    expect(within(card).queryByRole('button', { name: 'Confirm match' })).not.toBeInTheDocument()
  })
})

describe('who decided is shown apart from how the match began', () => {
  const confirmedAs = (decided: { kind: string; user_id: string | null }, created = decided, origin: PaymentMatch['origin'] = 'auto_rule') => {
    confirmedStore({ decided_by: decided, created_by: created, origin, reason: null,
      history: [{ action: 'confirmed', state: 'confirmed', amount: { amount: '10.00', currency: 'EUR' }, by: decided, reason: null, at: '2026-11-21T18:35:00Z' }] })
    start()
    return screen.findByRole('button', { name: 'Confirmed' }).then((b) => userEvent.click(b))
  }
  const cardOf = () => screen.findByRole('article', { name: 'Rent' })

  it('names an automatic confirmation as the engine, with a marker, and the bank marker like the month view', async () => {
    await confirmedAs({ kind: 'system', user_id: null })
    const card = await cardOf()
    expect(within(card).getByTestId('decision')).toHaveTextContent('Confirmed automatically by the matching engine, after its safeguards passed.')
    expect(card.querySelector('[data-decider="system"]')).toHaveTextContent('Decided by the engine')
    expect(card.querySelector('[data-provenance="bank"]')).toBeInTheDocument()
    expect(card).not.toHaveTextContent('Confirmed by you')
  })

  it("never calls a token's match the person's own, and says it was not made on this screen", async () => {
    await confirmedAs({ kind: 'token', user_id: null }, { kind: 'token', user_id: null }, 'user')
    const card = await cardOf()
    expect(card).toHaveTextContent('Created by an API token or MCP client')
    expect(card).not.toHaveTextContent(/by you/)
    expect(card.querySelector('[data-decider="token"]')).toHaveTextContent('Via API token or MCP')
  })

  it("calls the signed-in person's own confirmation \"you\", and another user's another person", async () => {
    await confirmedAs({ kind: 'user', user_id: '01JD3Y4C8V9M2Q7W5R6T0XZBNA' }, { kind: 'system', user_id: null })
    expect(within(await cardOf()).getByTestId('decision')).toHaveTextContent('Confirmed by you.')
    expect(within(await cardOf()).queryByText(/Decided by the engine/)).not.toBeInTheDocument()
  })

  it('writes the history as sentences, with an ISO UTC time and no raw codes', async () => {
    await confirmedAs({ kind: 'system', user_id: null })
    const card = await cardOf()
    await userEvent.click(within(card).getByText(/History/))
    expect(card).toHaveTextContent(/2026-11-21 18:35 UTC: Confirmed 10\.00 EUR.* by the matching engine/)
    expect(card).not.toHaveTextContent('confirmed → confirmed')
  })
})

describe('ambiguity and reasons in plain words', () => {
  it('groups suggestions that compete for one occurrence, says so, and turns the codes into sentences', async () => {
    const reason = 'automatic match not confirmed: several_transactions (2 candidate transactions for this occurrence); date_outside_window (9 days from the due date; at most 3); description similarity 80/100 (a hint only)'
    store = [match('a', { reason }), match('b', { occurrence_id: 'occ-a', transaction_id: 'tx-b', reason })]
    start()
    const cards = await screen.findAllByRole('article', { name: 'Rent' })
    expect(cards).toHaveLength(2)
    for (const card of cards) {
      expect(within(card).getByTestId('competing')).toHaveTextContent('2 suggestions compete for this occurrence')
      expect(card).toHaveTextContent('2 bank transactions could pay this occurrence, so a person has to choose.')
      expect(card).toHaveTextContent('It is 9 days from the due date; automatic confirmation allows at most 3.')
      expect(card).toHaveTextContent('The description looks 80% similar. That is a hint, not proof.')
      expect(card).not.toHaveTextContent('several_transactions')
      expect(card).not.toHaveTextContent('date_outside_window')
    }
  })

  it('does not call a single suggestion ambiguous', async () => {
    start()
    await screen.findByRole('article', { name: 'Rent' })
    expect(screen.queryByTestId('competing')).not.toBeInTheDocument()
  })
})

describe('focus stays put while a save is pending', () => {
  it('keeps keyboard focus on the Confirm button (aria-disabled, not disabled) and ignores a second press', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    extra['POST /api/v1/payment-matches/a/confirm'] = async () => {
      await gate
      confirmedStore()
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    const card = await screen.findByRole('article', { name: 'Rent' })
    const confirm = within(card).getByRole('button', { name: 'Confirm match' })
    confirm.focus()
    await user.keyboard('{Enter}')
    const busy = await within(card).findByRole('button', { name: 'Confirming…' })
    expect(busy).toHaveAttribute('aria-disabled', 'true')
    expect(busy).not.toBeDisabled()
    expect(busy).toHaveFocus()
    await user.keyboard('{Enter}')
    await act(async () => release())
    await screen.findByText(/Match confirmed/)
    expect(posts()).toHaveLength(1)
  })
})

describe('the notice after a save names what changed and is honest about Unmatch', () => {
  it('names the commitment and the month, and warns that the unmatch is final', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      confirmedStore()
      return ok(store[0])
    }
    start()
    await userEvent.click(within(await screen.findByRole('article', { name: 'Rent' })).getByRole('button', { name: 'Confirm match' }))
    const notice = (await screen.findByText(/Match confirmed/)).closest('[role="status"]')!
    expect(notice).toHaveTextContent('Match confirmed for Rent, November 2026')
    expect(notice).toHaveTextContent('Unmatching below is final for now')
    expect(within(notice as HTMLElement).queryByRole('button', { name: /^Undo/ })).not.toBeInTheDocument()
    expect(within(notice as HTMLElement).getByRole('button', { name: 'Unmatch (final)' })).toBeInTheDocument()
  })

  it('stays self-describing after switching view, and the unmatch says it closed the pair', async () => {
    extra['POST /api/v1/payment-matches/a/confirm'] = () => {
      confirmedStore()
      return ok(store[0])
    }
    extra['POST /api/v1/payment-matches/a/unmatch'] = () => {
      store = [match('a', { state: 'rejected', version: 3 })]
      return ok(store[0])
    }
    start()
    const user = userEvent.setup()
    await user.click(within(await screen.findByRole('article', { name: 'Rent' })).getByRole('button', { name: 'Confirm match' }))
    await screen.findByText(/Match confirmed/)
    await user.click(screen.getByRole('button', { name: 'Confirmed' }))
    await user.click((await screen.findAllByRole('button', { name: 'Unmatch (final)' }))[0]!)
    expect(await screen.findByText('Unmatched')).toBeInTheDocument()
    expect(screen.getByText(/this pair is closed and will not be suggested again/)).toHaveTextContent('Rent, November 2026')
  })
})

describe('the page says offline once', () => {
  it('shows one note and turns every action off', async () => {
    vi.spyOn(window.navigator, 'onLine', 'get').mockReturnValue(false)
    start()
    const card = await screen.findByRole('article', { name: 'Rent' })
    expect(document.querySelectorAll('#matches-offline-note')).toHaveLength(1)
    expect(card).not.toHaveTextContent(/offline/)
    expect(within(card).getByRole('button', { name: 'Confirm match' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Run matching now' })).toBeDisabled()
  })
})
