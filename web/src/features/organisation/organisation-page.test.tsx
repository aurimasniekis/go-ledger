import { QueryClient } from '@tanstack/react-query'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, fail, installApi, none, ok, renderApp, signedIn } from '@/test/fake-api'
import { def } from './fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const FOOD = def('category', 'F00', 'Food')
const GROC = def('category', 'F01', 'Groceries', { parent_id: FOOD.id })
const HOME = def('category', 'H00', 'Home')
const TAX = def('label', 'L00', 'Tax')
const HOUSEHOLD = def('bucket', 'B00', 'Household')
const page = (items: unknown[]) => ok({ items, next_cursor: null })

function routes(extra: Record<string, Parameters<typeof installApi>[0][string]> = {}, grants = ALL_GRANTS) {
  return {
    ...signedIn(undefined, grants),
    'GET /api/v1/categories': page([FOOD, GROC, HOME]),
    'GET /api/v1/labels': page([TAX]),
    'GET /api/v1/buckets': page([HOUSEHOLD]),
    ...extra,
  }
}

describe('organisation page', () => {
  it('lists categories as a two-level tree, labels and buckets, asking the server not to list archived ones', async () => {
    const calls = installApi(routes())
    renderApp('/organisation')
    const cats = await screen.findByRole('list', { name: 'categories' })
    expect(within(cats).getAllByRole('listitem').map((li) => li.querySelector('p')?.textContent)).toEqual(['Food', 'Groceries', 'Home'])
    // The child is nested under its parent.
    expect(within(within(cats).getAllByRole('listitem')[0]!).getByText('Groceries')).toBeInTheDocument()
    expect(within(await screen.findByRole('list', { name: 'labels' })).getByText('Tax')).toBeInTheDocument()
    expect(within(await screen.findByRole('list', { name: 'buckets' })).getByText('Household')).toBeInTheDocument()
    expect(calls.find((c) => c.path === '/api/v1/categories')!.query.get('archived')).toBe('false')
    expect(screen.getByText(/holds no money and allocates none/)).toBeInTheDocument()
  })

  it('offers no change to a role that can only read', async () => {
    installApi(routes({}, ['organisation:read']))
    renderApp('/organisation')
    await screen.findByRole('list', { name: 'categories' })
    expect(screen.queryByRole('button', { name: /^Add / })).toBeNull()
    expect(screen.queryByRole('button', { name: /^(Rename|Archive|Delete)/ })).toBeNull()
    expect(screen.getByText(/does not hold organisation:write/)).toBeInTheDocument()
  })

  it('says so, and shows nothing, when Ledger cannot be reached', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/categories': () => {
        throw new TypeError('offline')
      },
      'GET /api/v1/labels': page([]),
      'GET /api/v1/buckets': page([]),
    })
    renderApp('/organisation', new QueryClient({ defaultOptions: { queries: { retry: false } } }))
    expect(await screen.findByText(/no connection to the server/)).toBeInTheDocument()
  })

  it('adds a category under a parent and shows a duplicate name against the field', async () => {
    const user = userEvent.setup()
    let n = 0
    const calls = installApi(
      routes({
        'POST /api/v1/categories': () => (++n === 1 ? fail(409, 'duplicate_name', 'another category has this name'): ok(def('category', 'N00', 'Dining', { parent_id: FOOD.id }), 201)),
      }),
    )
    renderApp('/organisation')
    await user.click(await screen.findByRole('button', { name: 'Add category' }))
    const dlg = await screen.findByRole('dialog', { name: 'Add a category' })
    await user.type(within(dlg).getByLabelText('Name'), 'Food{Enter}')
    expect(await within(dlg).findByText('another category has this name')).toBeInTheDocument()
    expect(within(dlg).getByLabelText('Name')).toBeInvalid()
    await user.clear(within(dlg).getByLabelText('Name'))
    await user.type(within(dlg).getByLabelText('Name'), 'Dining')
    await user.selectOptions(within(dlg).getByLabelText('Parent category'), FOOD.id)
    await user.click(within(dlg).getByRole('button', { name: 'Add category' }))
    expect(await screen.findByText('Dining was added.')).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'POST').at(-1)!.body).toEqual({ name: 'Dining', parent_id: FOOD.id })
  })

  it('renames with If-Match and a merge patch of only what changed; a stale version reloads and says nothing was changed', async () => {
    const user = userEvent.setup()
    let stale = true
    const calls = installApi(
      routes({
        ['PATCH /api/v1/labels/' + TAX.id]: () => (stale ? fail(409, 'version_conflict', 'stale') : ok({ ...TAX, name: 'Taxes', version: 3 })),
      }),
    )
    renderApp('/organisation')
    await user.click(await screen.findByRole('button', { name: 'Rename Tax' }))
    const dlg = await screen.findByRole('dialog', { name: 'Rename Tax' })
    await user.clear(within(dlg).getByLabelText('Name'))
    await user.type(within(dlg).getByLabelText('Name'), 'Taxes{Enter}')
    expect(await within(dlg).findByText(/changed elsewhere.*Nothing was changed/)).toBeInTheDocument()
    stale = false
    await user.click(within(dlg).getByRole('button', { name: 'Save changes' }))
    expect(await screen.findByText('Taxes was saved.')).toBeInTheDocument()
    const patch = calls.filter((c) => c.method === 'PATCH').at(-1)!
    expect(patch.headers['If-Match']).toBe('"1"')
    expect(patch.headers['Content-Type']).toBe('application/merge-patch+json')
    expect(patch.body).toEqual({ name: 'Taxes' })
  })

  it('archives without a confirmation, offers Undo, and shows nothing as done when the server refuses', async () => {
    const user = userEvent.setup()
    let archived = false
    let refuse = false
    const calls = installApi(
      routes({
        'GET /api/v1/labels': () => page([{ ...TAX, archived, version: archived ? 2 : 1 }]),
        ['POST /api/v1/labels/' + TAX.id + '/archive']: () => {
          if (refuse) return fail(409, 'conflict', 'cannot archive right now')
          archived = true
          return ok({ ...TAX, archived: true, version: 2 })
        },
        ['POST /api/v1/labels/' + TAX.id + '/unarchive']: () => {
          archived = false
          return ok({ ...TAX, version: 3 })
        },
      }),
    )
    renderApp('/organisation')
    await user.click(await screen.findByRole('button', { name: 'Archive Tax' }))
    expect(await screen.findByText(/Tax is archived\. Everything assigned to it is kept/)).toBeInTheDocument()
    expect(calls.find((c) => c.path.endsWith('/archive'))!.headers['If-Match']).toBe('"1"')
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    expect(await screen.findByText('Tax is active again.')).toBeInTheDocument()
    expect(calls.find((c) => c.path.endsWith('/unarchive'))!.headers['If-Match']).toBe('"2"')
    refuse = true
    await user.click(await screen.findByRole('button', { name: 'Archive Tax' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('cannot archive right now')
    expect(screen.queryByText(/is archived\./)).toBeNull()
  })

  it('refuses to delete a definition in use until a replacement is chosen, then moves the assignments', async () => {
    const user = userEvent.setup()
    const calls = installApi(
      routes({
        ['DELETE /api/v1/categories/' + HOME.id]: (r) =>
          r.query.get('reassign_to') ? ok({ reassigned: 3 }) : fail(409, 'definition_in_use', 'Home is still assigned to 3 objects.'),
      }),
    )
    renderApp('/organisation')
    await user.click(await screen.findByRole('button', { name: 'Delete Home' }))
    const dlg = await screen.findByRole('dialog', { name: 'Delete Home' })
    await user.click(within(dlg).getByRole('button', { name: 'Delete Home' }))
    expect(await within(dlg).findByRole('alert')).toHaveTextContent('still assigned to 3 objects')
    const move = within(dlg).getByRole('button', { name: 'Move assignments and delete' })
    expect(move).toBeDisabled()
    const select = within(dlg).getByLabelText('Move the assignments to')
    expect(within(select).queryByRole('option', { name: 'Home' })).toBeNull() // not itself
    await user.selectOptions(select, FOOD.id)
    await user.click(move)
    expect(await screen.findByText(/Home was deleted; 3 assignments moved to the replacement/)).toBeInTheDocument()
    const del = calls.filter((c) => c.method === 'DELETE')
    expect(del[0]!.headers['If-Match']).toBe('"1"')
    expect(del[1]!.query.get('reassign_to')).toBe(FOOD.id)
  })

  it('shows the members of a bucket and removes one', async () => {
    const user = userEvent.setup()
    let members = [{ object_type: 'commitment', object_id: 'c-a', added_by: {}, added_at: '2026-11-21T10:00:00Z' }]
    const calls = installApi(
      routes({
        ['GET /api/v1/buckets/' + HOUSEHOLD.id + '/members']: () => page(members),
        'GET /api/v1/commitments/c-a': ok({ id: 'c-a', name: 'Rent' }),
        ['DELETE /api/v1/buckets/' + HOUSEHOLD.id + '/members/commitment/c-a']: () => {
          members = []
          return none()
        },
      }),
    )
    renderApp('/organisation')
    await user.click(await screen.findByRole('button', { name: 'Members of Household' }))
    const dlg = await screen.findByRole('dialog', { name: 'Members of Household' })
    expect(await within(dlg).findByText(/Commitment Rent/)).toBeInTheDocument()
    await user.click(within(dlg).getByRole('button', { name: /^Remove commitment c-a from Household/ }))
    expect(await within(dlg).findByText('Nothing is in this bucket.')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(true)
  })
})
