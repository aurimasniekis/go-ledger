import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { fail, installApi, none, ok, renderApp, signedIn } from '@/test/fake-api'
import { commitment } from './fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const page = (items: unknown[]) => ok({ items, next_cursor: null })
const READ_ONLY = ['commitments:read']

// The form is read-only until the session's grants have loaded, so typing waits for it.
const ready = async () => {
  const name = await screen.findByLabelText('Name')
  await waitFor(() => expect(name).toBeEnabled())
  return name
}

async function fillNew(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await ready(), 'Hosting')
  await user.type(screen.getByLabelText('Amount per due date'), '1200.500')
  await user.type(screen.getByLabelText('Currency'), 'EUR')
  await user.type(screen.getByLabelText('Day of the month'), '15')
  await user.type(screen.getByLabelText('Start date'), '2026-11-01')
}

describe('create a commitment', () => {
  it('sends the exact decimal text and shows the created commitment', async () => {
    const user = userEvent.setup()
    const created = commitment({ id: 'c-new', name: 'Hosting', amount: { amount: '1200.500', currency: 'EUR' }, start_date: '2026-11-01' })
    const calls = installApi({
      ...signedIn(),
      'POST /api/v1/commitments': ok(created, 201),
      'GET /api/v1/commitments/c-new': ok(created),
    })
    renderApp('/commitments/new')
    await fillNew(user)
    await user.click(screen.getByRole('button', { name: 'Create commitment' }))
    expect(await screen.findByRole('heading', { name: 'Hosting' })).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.body).toEqual({
      name: 'Hosting', kind: 'subscription', amount: { amount: '1200.500', currency: 'EUR' }, schedule: { version: 1, frequency: 'monthly', interval: 1, day: 15 },
      start_date: '2026-11-01', end_date: null, priority: null, notes: null,
    })
    expect(screen.getAllByText('Commitment created.').length).toBeGreaterThan(0)
    expect(screen.getByRole('link', { name: 'monthly view' })).toHaveAttribute('href', '/month?month=2026-11')
  })

  it('checks the form before sending and names every problem next to its field', async () => {
    const user = userEvent.setup()
    const calls = installApi({ ...signedIn() })
    renderApp('/commitments/new')
    await user.click(await screen.findByRole('button', { name: 'Create commitment' }))
    expect(await screen.findByText('Give the commitment a name.')).toBeInTheDocument()
    expect(screen.getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByText(/Enter an amount greater than zero/)).toBeInTheDocument()
    expect(screen.getByText(/Enter the currency as three capital letters/)).toBeInTheDocument()
    expect(screen.getByText(/Enter the start date as YYYY-MM-DD/)).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
  })

  it('shows a structured server error per field, keeps what was typed and says nothing was saved', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'POST /api/v1/commitments': fail(422, 'validation_failed', 'amount: bad', {
        field: 'amount.amount',
        errors: [{ field: 'amount.amount', message: 'amount must be greater than zero' }, { field: 'schedule.day', message: 'day 31 does not exist in February' }, { field: 'leeway.kind', message: 'kind must be one of x' }],
      }),
    })
    renderApp('/commitments/new')
    await fillNew(user)
    await user.click(screen.getByRole('button', { name: 'Create commitment' }))
    expect(await screen.findByText('amount must be greater than zero')).toBeInTheDocument()
    expect(screen.getByText('day 31 does not exist in February')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount per due date')).toHaveValue('1200.500')
    expect(screen.getByLabelText('Name')).toHaveValue('Hosting')
    expect(screen.getByText(/Nothing was saved/)).toBeInTheDocument()
    expect(screen.getByText('Not saved')).toBeInTheDocument()
    // Typing in a field clears its server message.
    await user.type(screen.getByLabelText('Day of the month'), '0')
    expect(screen.queryByText('day 31 does not exist in February')).toBeNull()
  })

  it('keeps every entry when the server cannot be reached', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn() })
    renderApp('/commitments/new')
    await fillNew(user)
    // Cut the network after sign-in data loaded.
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))))
    await user.click(screen.getByRole('button', { name: 'Create commitment' }))
    expect(await screen.findByText(/Cannot reach the server. Nothing was saved/)).toBeInTheDocument()
    expect(screen.getByLabelText('Name')).toHaveValue('Hosting')
    expect(screen.getByRole('button', { name: 'Create commitment' })).toBeEnabled()
  })

  it('shows the pending state while the server has not answered', async () => {
    const user = userEvent.setup()
    let release: (r: { status: number; json: unknown }) => void = () => {}
    installApi({ ...signedIn(), 'POST /api/v1/commitments': () => new Promise((res) => (release = res)) })
    renderApp('/commitments/new')
    await fillNew(user)
    await user.click(screen.getByRole('button', { name: 'Create commitment' }))
    expect(await screen.findByRole('button', { name: 'Saving…' })).toBeDisabled()
    expect(screen.getByText('Not saved yet')).toBeInTheDocument()
    release({ status: 422, json: { code: 'validation_failed', message: 'name: x', errors: [{ field: 'name', message: 'name is taken' }], request_id: 'r' } })
    expect(await screen.findByText('name is taken')).toBeInTheDocument()
  })

  it('shows an annual schedule with a month, and a grace period only when chosen', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn() })
    renderApp('/commitments/new')
    await ready()
    await user.selectOptions(screen.getByLabelText('Repeats'), 'annual')
    expect(screen.getByLabelText('Month')).toBeInTheDocument()
    expect(screen.queryByLabelText('Every how many months')).toBeNull()
    expect(screen.queryByLabelText('Days of grace')).toBeNull()
    await user.selectOptions(screen.getByLabelText('Grace'), 'period')
    expect(screen.getByLabelText('Days of grace')).toBeInTheDocument()
  })

  it('offers no form to a role without commitments:write', async () => {
    installApi({ ...signedIn(undefined, READ_ONLY) })
    renderApp('/commitments/new')
    expect(await screen.findByText(/lacks commitments:write/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create commitment' })).toBeNull()
  })
})

describe('edit a commitment', () => {
  const routes = (c = commitment(), more: Parameters<typeof installApi>[0] = {}) => ({ ...signedIn(), 'GET /api/v1/commitments/c-rent': ok(c), ...more })

  it('patches only what changed with the version it was loaded at, and explains the effect on occurrences', async () => {
    const user = userEvent.setup()
    const saved = commitment({ amount: { amount: '810.00', currency: 'EUR' }, version: 4 })
    const calls = installApi(routes(commitment(), { 'PATCH /api/v1/commitments/c-rent': ok(saved) }))
    renderApp('/commitments/c-rent')
    const amount = await screen.findByLabelText('Amount per due date')
    expect(screen.getByTestId('occurrence-note')).toHaveTextContent('Nothing has changed yet.')
    await user.clear(amount)
    await user.type(amount, '810.00')
    expect(screen.getByTestId('occurrence-note')).toHaveTextContent(/Occurrences already marked paid never change/)
    await user.type(screen.getByLabelText(/Apply changed terms from/), '2026-12-01')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    expect((await screen.findAllByText('Changes saved.')).length).toBeGreaterThan(0)
    const patch = calls.find((c) => c.method === 'PATCH')!
    expect(patch.body).toEqual({ amount: { amount: '810.00', currency: 'EUR' }, effective_from: '2026-12-01', version: 3 })
    expect(patch.headers['Content-Type']).toBe('application/merge-patch+json')
  })

  it('says a name-only edit does not touch occurrences', async () => {
    const user = userEvent.setup()
    installApi(routes())
    renderApp('/commitments/c-rent')
    await user.type(await screen.findByLabelText('Name'), ' 2')
    expect(screen.getByTestId('occurrence-note')).toHaveTextContent(/do not touch any occurrence/)
  })

  it('does not send an edit with no change', async () => {
    const user = userEvent.setup()
    const calls = installApi(routes())
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('Nothing has changed, so there is nothing to save.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
  })

  it('shows the server field error on the effective date and keeps the entries', async () => {
    const user = userEvent.setup()
    installApi(routes(commitment(), {
      'PATCH /api/v1/commitments/c-rent': fail(409, 'pending_terms', 'a terms change effective 2027-01-01 is pending', { field: 'effective_from' }),
    }))
    renderApp('/commitments/c-rent')
    const amount = await screen.findByLabelText('Amount per due date')
    await user.clear(amount)
    await user.type(amount, '9')
    await user.type(screen.getByLabelText(/Apply changed terms from/), '2026-12-01')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('a terms change effective 2027-01-01 is pending')).toBeInTheDocument()
    expect(screen.getByLabelText('Amount per due date')).toHaveValue('9')
  })

  it('on a version conflict keeps the entries, refuses to resend and offers the latest version', async () => {
    const user = userEvent.setup()
    const calls = installApi(routes(commitment(), {
      'PATCH /api/v1/commitments/c-rent': fail(409, 'version_conflict', 'changed', { reconciliation: { current_version: 5 } }),
    }))
    renderApp('/commitments/c-rent')
    await user.type(await screen.findByLabelText('Name'), '!')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText(/changed somewhere else/)).toBeInTheDocument()
    expect(screen.getByLabelText('Name')).toHaveValue('Rent!')
    expect(calls.filter((c) => c.method === 'PATCH')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: 'Load the latest version' }))
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveValue('Rent'))
  })

  it('shows a read-only form without commitments:write and hides stop actions', async () => {
    installApi({ ...signedIn(undefined, READ_ONLY), 'GET /api/v1/commitments/c-rent': ok(commitment()) })
    renderApp('/commitments/c-rent')
    expect(await screen.findByLabelText('Name')).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Save changes' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'End commitment' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Delete commitment' })).toBeNull()
  })

  it('says so when the commitment does not exist', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/commitments/c-rent': fail(404, 'not_found', 'no such commitment') })
    renderApp('/commitments/c-rent')
    expect(await screen.findByRole('heading', { name: 'Commitment not found' })).toBeInTheDocument()
  })
})

describe('end and delete', () => {
  it('ends a commitment with a date, confirms it and undoes it', async () => {
    const user = userEvent.setup()
    const ended = commitment({ end_date: '2026-12-31', version: 4 })
    const restored = commitment({ version: 5 })
    let get = commitment()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': () => ok(get),
      'PATCH /api/v1/commitments/c-rent': (req) => {
        const b = req.body as { end_date: string | null }
        get = b.end_date ? ended : restored
        return ok(get)
      },
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'End commitment' }))
    const dialog = await screen.findByRole('dialog', { name: 'End Rent' })
    const date = within(dialog).getByLabelText('End date')
    await user.clear(date)
    await user.type(date, '2026-12-31')
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    expect(await screen.findByText(/now ends on/)).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PATCH')[0]!.body).toEqual({ end_date: '2026-12-31', version: 3 })
    await user.click(screen.getByRole('button', { name: /Undo/ }))
    expect((await screen.findAllByText('End date restored.')).length).toBeGreaterThan(0)
    expect(calls.filter((c) => c.method === 'PATCH')[1]!.body).toEqual({ end_date: null, version: 4 })
  })

  it('refuses an end date before the start date without asking the server, and shows a server refusal in the dialog', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'PATCH /api/v1/commitments/c-rent': fail(422, 'validation_failed', 'end_date: no', { field: 'end_date', errors: [{ field: 'end_date', message: 'end_date must not be before start_date' }] }),
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'End commitment' }))
    const dialog = await screen.findByRole('dialog', { name: 'End Rent' })
    const date = within(dialog).getByLabelText('End date')
    await user.clear(date)
    await user.type(date, '2025-01-01')
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    expect(await within(dialog).findByText('The end date cannot be before the start date.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    await user.clear(date)
    await user.type(date, '2099-02-01')
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    expect(await within(dialog).findByText('end_date must not be before start_date')).toBeInTheDocument()
  })

  it('deletes after a confirmation, with If-Match, and returns to the list', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'DELETE /api/v1/commitments/c-rent': none(),
      'GET /api/v1/commitments': page([]),
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'Delete commitment' }))
    const dialog = await screen.findByRole('alertdialog')
    expect(dialog).toHaveTextContent(/cannot be undone/)
    await user.click(within(dialog).getByRole('button', { name: 'Delete commitment' }))
    expect(await screen.findByRole('heading', { name: 'Commitments' })).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'DELETE')!.headers['If-Match']).toBe('"3"')
  })

  it('keeps the commitment and says why when the server refuses the delete', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'DELETE /api/v1/commitments/c-rent': fail(409, 'conflict', 'the commitment has dependent records and cannot be deleted'),
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'Delete commitment' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Delete commitment' }))
    expect(await within(dialog).findByText(/dependent records and cannot be deleted/)).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Rent', hidden: true })).toBeInTheDocument()
  })
})

describe('honest consequences, unsaved entries and announced errors', () => {
  it('says what ending does to overdue unpaid occurrences, and needs a confirmation for a date before today', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'PATCH /api/v1/commitments/c-rent': ok(commitment({ end_date: '2026-02-01', version: 4 })),
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'End commitment' }))
    const dialog = await screen.findByRole('dialog', { name: 'End Rent' })
    expect(dialog).toHaveTextContent(/even when they are overdue/)
    expect(dialog).not.toHaveTextContent(/overdue ones stay/)
    const date = within(dialog).getByLabelText('End date')
    await user.clear(date)
    await user.type(date, '2026-02-01')
    expect(within(dialog).getByRole('alert')).toHaveTextContent(/before today.*including overdue ones/)
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    expect(within(dialog).getByText('Confirm this before ending the commitment.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    await user.click(within(dialog).getByRole('checkbox', { name: /I understand/ }))
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    expect(await screen.findByText(/now ends on/)).toHaveTextContent(/overdue ones included.*only from today on/)
    expect(calls.filter((c) => c.method === 'PATCH')[0]!.body).toEqual({ end_date: '2026-02-01', version: 3 })
  })

  it('warns, and needs a confirmation, when the form moves the end date before today or the start date later', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'PATCH /api/v1/commitments/c-rent': ok(commitment({ start_date: '2026-03-01', version: 4 })),
    })
    renderApp('/commitments/c-rent')
    const start = await screen.findByLabelText('Start date')
    await waitFor(() => expect(start).toBeEnabled())
    await user.clear(start)
    await user.type(start, '2026-03-01')
    expect(screen.getByTestId('occurrence-note')).toHaveTextContent(/whatever the date below.*overdue ones too/)
    expect(screen.getByTestId('removal-warning')).toHaveTextContent(/start date moves later.*including overdue ones/)
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('Confirm this before saving.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    await user.click(screen.getByRole('checkbox', { name: /I understand/ }))
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH')).toBe(true))
  })

  it('does not let End, Undo or Delete discard unsaved entries', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn(), 'GET /api/v1/commitments/c-rent': ok(commitment()) })
    renderApp('/commitments/c-rent')
    const name = await ready()
    expect(screen.getByRole('button', { name: 'End commitment' })).toBeEnabled()
    await user.type(name, ' Two')
    expect(screen.getByRole('button', { name: 'End commitment' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Delete commitment' })).toBeDisabled()
    expect(screen.getByText(/unsaved changes in the form above/)).toBeInTheDocument()
    expect(name).toHaveValue('Rent Two')
  })

  it('keeps unsaved entries made after an End when Undo is pressed: Undo waits until the form is saved or reloaded', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': ok(commitment()),
      'PATCH /api/v1/commitments/c-rent': ok(commitment({ end_date: '2026-12-31', version: 4 })),
    })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'End commitment' }))
    const dialog = await screen.findByRole('dialog', { name: 'End Rent' })
    await user.clear(within(dialog).getByLabelText('End date'))
    await user.type(within(dialog).getByLabelText('End date'), '2099-12-31')
    await user.click(within(dialog).getByRole('button', { name: 'End commitment' }))
    await screen.findByText(/now ends on/)
    const undo = screen.getByRole('button', { name: /Undo/ })
    expect(undo).toBeEnabled()
    await user.type(await screen.findByLabelText('Name'), ' Two')
    expect(undo).toBeDisabled()
    expect(screen.getByLabelText('Name')).toHaveValue('Rent Two')
  })

  it('announces a failed check with a count and focuses the first invalid control', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn() })
    renderApp('/commitments/new')
    await user.click(await screen.findByRole('button', { name: 'Create commitment' }))
    const alert = await screen.findByText(/fields need attention/)
    expect(alert.closest('[role="alert"]')).not.toBeNull()
    expect(alert).toHaveTextContent(/^[2-9]\d* fields need attention/)
    await waitFor(() => expect(screen.getByLabelText('Name')).toHaveFocus())
    expect(screen.getByLabelText('Name')).toHaveAttribute('aria-invalid', 'true')
  })

  it('clears the Saved note once the user edits again', async () => {
    const user = userEvent.setup()
    let get = commitment()
    installApi({
      ...signedIn(),
      'GET /api/v1/commitments/c-rent': () => ok(get),
      'PATCH /api/v1/commitments/c-rent': () => ok((get = commitment({ notes: 'x', version: 4 }))),
    })
    renderApp('/commitments/c-rent')
    await user.type(await screen.findByLabelText('Notes (optional)'), 'x')
    await user.click(screen.getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('Saved')).toBeInTheDocument()
    await user.type(await screen.findByLabelText('Notes (optional)'), 'y')
    await waitFor(() => expect(screen.queryByText('Saved')).toBeNull())
  })

  it('names the assignments in the delete confirmation', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn(), 'GET /api/v1/commitments/c-rent': ok(commitment()) })
    renderApp('/commitments/c-rent')
    await user.click(await screen.findByRole('button', { name: 'Delete commitment' }))
    expect(await screen.findByRole('alertdialog')).toHaveTextContent(/category, label and bucket assignments/)
  })
})

describe('the commitments list and the month view lead to the form', () => {
  it('shows amount and schedule on the rows with New commitment and Edit for a writer', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/commitments': page([commitment()]) })
    renderApp('/commitments')
    const list = await screen.findByRole('list', { name: 'Commitments' })
    expect(within(list).getByText('Monthly on the 2nd')).toBeInTheDocument()
    expect(within(list).getAllByText(/800\.00/).length).toBeGreaterThan(0)
    expect(within(list).getByRole('link', { name: 'Edit Rent' })).toHaveAttribute('href', '/commitments/c-rent')
    expect(screen.getByRole('link', { name: 'New commitment' })).toHaveAttribute('href', '/commitments/new')
  })

  it('hides New commitment and says View for a reader', async () => {
    installApi({ ...signedIn(undefined, ['commitments:read', 'organisation:read']), 'GET /api/v1/commitments': page([commitment()]) })
    renderApp('/commitments')
    const list = await screen.findByRole('list', { name: 'Commitments' })
    expect(within(list).getByRole('link', { name: 'View Rent' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'New commitment' })).toBeNull()
  })

  it('links New commitment from an empty list', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/commitments': page([]) })
    renderApp('/commitments')
    expect(await screen.findByText('No commitments yet')).toBeInTheDocument()
    expect(screen.getAllByRole('link', { name: 'New commitment' }).length).toBeGreaterThan(0)
  })
})
