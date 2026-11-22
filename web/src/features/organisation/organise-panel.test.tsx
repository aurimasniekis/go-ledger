import { QueryClient } from '@tanstack/react-query'
import { screen, within, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, fail, installApi, none, ok, renderApp, signedIn } from '@/test/fake-api'
import { item, summary } from '@/features/month/fixtures'
import { assignments, catAssignment, def, labelAssignment } from './fixtures'
import type { ObjectAssignments } from './types'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const FOOD = def('category', 'F00', 'Food')
const GROC = def('category', 'F01', 'Groceries', { parent_id: FOOD.id })
const OLD = def('category', 'O00', 'Old', { archived: true })
const TAX = def('label', 'L00', 'Tax')
const GIFT = def('label', 'L01', 'Gift')
const HOUSEHOLD = def('bucket', 'B00', 'Household')
const LOANS = def('bucket', 'B01', 'Loans')
const page = (items: unknown[]) => ok({ items, next_cursor: null })
const OBJ = '/api/v1/organisation/commitment/c-a'

function start(state: { a: ObjectAssignments }, extra: Record<string, Parameters<typeof installApi>[0][string]> = {}, grants = ALL_GRANTS, client?: QueryClient) {
  const calls = installApi({
    ...signedIn(undefined, grants),
    'GET /api/v1/occurrences/summary': ok(summary([item('a'), item('b')])),
    'GET /api/v1/categories': (r) => page(r.query.get('archived') === 'false' ? [FOOD, GROC] : [FOOD, GROC, OLD]),
    'GET /api/v1/labels': page([TAX, GIFT]),
    'GET /api/v1/buckets': page([HOUSEHOLD, LOANS]),
    ['GET ' + OBJ]: () => ok(state.a),
    ...extra,
  })
  renderApp('/month?month=2026-11', client)
  return calls
}

async function openPanel(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: 'Organise Rent a' }))
  return screen.findByRole('group', { name: 'Organisation of Rent a' })
}

describe('Organise control', () => {
  it('reads nothing until it is opened, then shows an automatic category with its origin and says choosing overrides it', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ category: catAssignment(GROC, { origin: 'rule', source: 'Supermarkets' }) }) }
    const calls = start(state)
    await screen.findByText('Rent a')
    expect(calls.some((c) => c.path.startsWith('/api/v1/organisation/'))).toBe(false)
    const panel = await openPanel(user)
    expect(await within(panel).findByText('Food › Groceries', { selector: 'strong' })).toBeInTheDocument()
    expect(within(panel).getByText(/Automatic, by a rule: Supermarkets/)).toBeInTheDocument()
    expect(within(panel).getByText(/Choose a category below to override it/)).toBeInTheDocument()
    // An automatic category is not the person's, so there is nothing to hand back.
    expect(within(panel).queryByRole('button', { name: 'Hand back to automatic' })).toBeNull()
  })

  it('sets the category as the person\'s choice, shown as saved only after the server answers, and hands it back', async () => {
    const user = userEvent.setup()
    const state = { a: assignments() }
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    const calls = start(state, {
      ['PUT ' + OBJ + '/category']: async (r) => {
        await gate
        state.a = assignments({ category: catAssignment(GROC) })
        expect((r.body as { category_id: string }).category_id).toBe(GROC.id)
        return ok(state.a)
      },
      ['DELETE ' + OBJ + '/category']: () => {
        state.a = assignments()
        return ok(state.a)
      },
    })
    const panel = await openPanel(user)
    expect(await within(panel).findByText(/No category yet/)).toBeInTheDocument()
    const select = within(panel).getByLabelText('Category for Rent a')
    expect(within(select).queryByRole('option', { name: 'Old' })).toBeNull() // archived: nothing new may use it
    await user.selectOptions(select, GROC.id)
    await user.click(within(panel).getByRole('button', { name: 'Set category' }))
    // Pending: not yet saved, and nothing claims it is.
    expect(await within(panel).findByText('Saving…', { selector: 'p' })).toHaveAttribute('role', 'status')
    expect(within(panel).queryByText(/^Saved:/)).toBeNull()
    release()
    expect(await within(panel).findByText('Saved: Food › Groceries is your choice for Rent a.')).toBeInTheDocument()
    expect(within(panel).getByText(/Your choice/)).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: 'Hand back to automatic' }))
    expect(await within(panel).findByText('Saved: Rent a is back to automatic assignment.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE' && c.path === OBJ + '/category')).toBe(true)
  })

  it('records "no category" as a choice (null), and shows a refusal without claiming it was saved', async () => {
    const user = userEvent.setup()
    const state = { a: assignments() }
    let refuse = true
    const calls = start(state, {
      ['PUT ' + OBJ + '/category']: () => {
        if (refuse) return fail(409, 'definition_archived', 'the category is archived')
        state.a = assignments({ category: catAssignment(null) })
        return ok(state.a)
      },
    })
    const panel = await openPanel(user)
    await user.selectOptions(await within(panel).findByLabelText('Category for Rent a'), 'No category')
    await user.click(within(panel).getByRole('button', { name: 'Set category' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('the category is archived')
    expect(within(panel).queryByText(/^Saved:/)).toBeNull()
    refuse = false
    await user.selectOptions(within(panel).getByLabelText('Category for Rent a'), 'No category')
    await user.click(within(panel).getByRole('button', { name: 'Set category' }))
    expect(await within(panel).findByText(/Saved: no category is your choice/)).toBeInTheDocument()
    expect(within(panel).getByText('No category (you chose none)')).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PUT').at(-1)!.body).toEqual({ category_id: null })
  })

  it('does not offer to reset an inherited category and says where to do it', async () => {
    const user = userEvent.setup()
    start({ a: assignments({ category: catAssignment(FOOD, { inherited: true }) }) })
    const panel = await openPanel(user)
    expect(await within(panel).findByText(/inherited from the pending transaction this booking replaced/)).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Hand back to automatic' })).toBeNull()
    expect(within(panel).getByText(/reset it on that transaction/)).toBeInTheDocument()
  })

  it('adds, removes and puts back labels; a removed label is remembered', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ labels: [labelAssignment(TAX, { origin: 'model', source: 'a model' })] }) }
    const calls = start(state, {
      ['PUT ' + OBJ + '/labels/' + GIFT.id]: () => {
        state.a = assignments({ labels: [...state.a.labels, labelAssignment(GIFT)], removed_labels: state.a.removed_labels })
        return ok(state.a)
      },
      ['DELETE ' + OBJ + '/labels/' + TAX.id]: () => {
        state.a = assignments({ labels: state.a.labels.filter((l) => l.label.id !== TAX.id), removed_labels: [labelAssignment(TAX)] })
        return ok(state.a)
      },
      ['PUT ' + OBJ + '/labels/' + TAX.id]: () => {
        state.a = assignments({ labels: [labelAssignment(TAX)] })
        return ok(state.a)
      },
    })
    const panel = await openPanel(user)
    expect(await within(panel).findByText(/Automatic, by a model: a model/)).toBeInTheDocument()
    const add = within(panel).getByLabelText('Label to add to Rent a')
    expect(within(add).queryByRole('option', { name: 'Tax' })).toBeNull() // already on it
    await user.selectOptions(add, GIFT.id)
    await user.click(within(panel).getByRole('button', { name: 'Add label' }))
    expect(await within(panel).findByText('Saved: Gift is on Rent a.')).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: 'Remove label Tax from Rent a' }))
    expect(await within(panel).findByText(/automatic runs will not add it back/)).toBeInTheDocument()
    const removed = within(panel).getByRole('list', { name: 'Removed labels' })
    await user.click(within(removed).getByRole('button', { name: 'Put label Tax back on Rent a' }))
    expect(await within(panel).findByText('Saved: Tax is on Rent a again.')).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PUT').map((c) => c.path)).toEqual([OBJ + '/labels/' + GIFT.id, OBJ + '/labels/' + TAX.id])
  })

  it('puts a commitment in a bucket and takes it out, reloading what is assigned', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ buckets: [HOUSEHOLD] }) }
    const calls = start(state, {
      ['PUT /api/v1/buckets/' + LOANS.id + '/members/commitment/c-a']: () => {
        state.a = assignments({ buckets: [HOUSEHOLD, LOANS] })
        return none()
      },
      ['DELETE /api/v1/buckets/' + HOUSEHOLD.id + '/members/commitment/c-a']: () => {
        state.a = assignments({ buckets: state.a.buckets.filter((b) => b.id !== HOUSEHOLD.id) })
        return none()
      },
    })
    const panel = await openPanel(user)
    expect(await within(panel).findByText('Household')).toBeInTheDocument()
    await user.selectOptions(within(panel).getByLabelText('Bucket to put Rent a in'), LOANS.id)
    await user.click(within(panel).getByRole('button', { name: 'Add to bucket' }))
    await waitFor(() => expect(within(within(panel).getByRole('list', { name: 'Buckets' })).getByText('Loans')).toBeInTheDocument())
    expect(within(panel).getByText('Saved: Rent a is in Loans.')).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: 'Take Rent a out of bucket Household' }))
    await waitFor(() => expect(within(within(panel).getByRole('list', { name: 'Buckets' })).queryByText('Household')).toBeNull())
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(1)
  })

  it('without organisation:write it shows what is assigned and offers no change', async () => {
    const user = userEvent.setup()
    start({ a: assignments({ category: catAssignment(FOOD), labels: [labelAssignment(TAX)], buckets: [HOUSEHOLD] }) }, {}, ['commitments:read', 'organisation:read'])
    const panel = await openPanel(user)
    expect(await within(panel).findByText('Food')).toBeInTheDocument()
    expect(within(panel).queryByRole('button')).toBeNull()
    expect(within(panel).queryByRole('combobox')).toBeNull()
  })

  it('is not offered without organisation:read', async () => {
    start({ a: assignments() }, {}, ['commitments:read'])
    await screen.findByText('Rent a')
    expect(screen.queryByRole('button', { name: /^Organise/ })).toBeNull()
  })

  it('says it could not load, with a way to retry, and shows no assignments as if there were none', async () => {
    const user = userEvent.setup()
    let down = true
    start({ a: assignments() }, { ['GET ' + OBJ]: () => (down ? fail(500, 'internal', 'boom') : ok(assignments())) }, ALL_GRANTS, new QueryClient({ defaultOptions: { queries: { retry: false } } }))
    await user.click(await screen.findByRole('button', { name: 'Organise Rent a' }))
    const panel = await screen.findByRole('group', { name: 'Organisation of Rent a' })
    expect(await within(panel).findByText(/Could not load/)).toBeInTheDocument()
    expect(within(panel).queryByText(/No category yet/)).toBeNull()
    down = false
    await user.click(within(panel).getByRole('button', { name: 'Try again' }))
    expect(await within(panel).findByText(/No category yet/)).toBeInTheDocument()
  })
})

describe('bucket filter on the monthly view', () => {
  it('narrows the occurrences to a bucket, says the totals still cover the month, and never shows every row while loading', async () => {
    const user = userEvent.setup()
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    start({ a: assignments() }, {
      ['GET /api/v1/buckets/' + HOUSEHOLD.id + '/members']: async () => {
        await gate
        return page([{ object_type: 'commitment', object_id: 'c-b', added_by: {}, added_at: '2026-11-21T10:00:00Z' }, { object_type: 'account', object_id: 'c-a', added_by: {}, added_at: '2026-11-21T10:00:00Z' }])
      },
    })
    await screen.findByText('Rent a')
    await user.selectOptions(await screen.findByLabelText('Show only commitments in bucket'), HOUSEHOLD.id)
    expect(await screen.findByText(/Loading the members of Household/)).toBeInTheDocument()
    expect(screen.queryByText('Rent a')).toBeNull()
    release()
    expect(await screen.findByText(/Showing 1 of 2 occurrences: those of commitments in Household/)).toBeInTheDocument()
    expect(screen.getByText(/The totals above still cover the whole month/)).toBeInTheDocument()
    expect(screen.getByText('Rent b')).toBeInTheDocument()
    expect(screen.queryByText('Rent a')).toBeNull() // an account with the same id is not that commitment
    await user.selectOptions(screen.getByLabelText('Show only commitments in bucket'), '')
    expect(await screen.findByText('Rent a')).toBeInTheDocument()
  })

  it('says so and shows every occurrence when the members cannot be loaded', async () => {
    const user = userEvent.setup()
    start({ a: assignments() }, { ['GET /api/v1/buckets/' + HOUSEHOLD.id + '/members']: fail(500, 'internal', 'boom') })
    await user.selectOptions(await screen.findByLabelText('Show only commitments in bucket'), HOUSEHOLD.id)
    expect(await screen.findByText(/could not be loaded, so the filter is not applied/, undefined, { timeout: 5000 })).toBeInTheDocument()
    expect(screen.getByText('Rent a')).toBeInTheDocument()
  }, 10000)
})
