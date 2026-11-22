import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, installApi, ok, renderApp, signedIn } from '@/test/fake-api'
import { assignments, catAssignment, def, labelAssignment } from '@/features/organisation/fixtures'
import { merchant } from './fixtures'

// Category and label assignment on a merchant record (task O5): keyed by the record id, never by a name, behind the
// administrator-only merchants grants as well as the organisation grants.
afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const LIDL = merchant('L00', 'Lidl')
const FOOD = def('category', 'F00', 'Food')
const TAX = def('label', 'L00', 'Tax')
const OBJ = '/api/v1/organisation/merchant/' + LIDL.id
const page = (items: unknown[]) => ok({ items, next_cursor: null })

function start(state: { a: ReturnType<typeof assignments> }, grants = ALL_GRANTS, rec = LIDL, extra: Parameters<typeof installApi>[0] = {}) {
  const calls = installApi({
    ...signedIn(undefined, grants),
    ['GET /api/v1/merchants/' + LIDL.id]: ok(rec),
    'GET /api/v1/categories': page([FOOD]),
    'GET /api/v1/labels': page([TAX]),
    'GET /api/v1/buckets': page([]),
    ['GET ' + OBJ]: () => ok(state.a),
    ...extra,
  })
  renderApp('/merchants/' + LIDL.id)
  return calls
}

describe('organising a merchant', () => {
  it('sets a category and a label by the merchant id, offers no buckets, and says saved only after the answer', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ object_type: 'merchant', object_id: LIDL.id }) }
    const calls = start(state, ALL_GRANTS, LIDL, {
      ['PUT ' + OBJ + '/category']: (r) => {
        expect(r.body).toEqual({ category_id: FOOD.id })
        state.a = assignments({ object_type: 'merchant', object_id: LIDL.id, category: catAssignment(FOOD) })
        return ok(state.a)
      },
      ['PUT ' + OBJ + '/labels/' + TAX.id]: () => {
        state.a = assignments({ object_type: 'merchant', object_id: LIDL.id, category: catAssignment(FOOD), labels: [labelAssignment(TAX)] })
        return ok(state.a)
      },
    })
    await screen.findByRole('heading', { name: 'Lidl', level: 1 })
    expect(calls.some((c) => c.path.startsWith('/api/v1/organisation/'))).toBe(false)
    await user.click(screen.getByRole('button', { name: 'Organise Lidl' }))
    const panel = await screen.findByRole('group', { name: 'Organisation of Lidl' })
    expect(within(panel).queryByRole('heading', { name: 'Buckets' })).toBeNull()
    await user.selectOptions(await within(panel).findByLabelText('Category for Lidl'), FOOD.id)
    await user.click(within(panel).getByRole('button', { name: 'Set category' }))
    expect(await within(panel).findByText('Saved: Food is your choice for Lidl.')).toBeInTheDocument()
    await user.selectOptions(within(panel).getByLabelText('Label to add to Lidl'), TAX.id)
    await user.click(within(panel).getByRole('button', { name: 'Add label' }))
    expect(await within(panel).findByText('Saved: Tax is on Lidl.')).toBeInTheDocument()
    expect(calls.every((c) => !c.path.includes('/organisation/merchant/lidl'))).toBe(true)
  })

  it('is read-only with organisation:write but not merchants:write, and absent without merchants:read', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ object_type: 'merchant', object_id: LIDL.id, category: catAssignment(FOOD) }) }
    start(state, ['organisation:read', 'organisation:write', 'merchants:read'])
    await user.click(await screen.findByRole('button', { name: 'Organise Lidl' }))
    const panel = await screen.findByRole('group', { name: 'Organisation of Lidl' })
    expect(await within(panel).findByText('Food', { selector: 'strong' })).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Set category' })).toBeNull()
  })

  it('is not offered without merchants:read, whatever the organisation grants allow', async () => {
    start({ a: assignments() }, ['organisation:read', 'organisation:write', 'commitments:read'], LIDL)
    // The page itself needs merchants:read, so it says why and offers nothing.
    expect(await screen.findByText(/does not hold merchants:read/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Organise Lidl' })).toBeNull()
  })

  it('reads an archived merchant but does not change it, and a merged one has no Organise control', async () => {
    const user = userEvent.setup()
    const state = { a: assignments({ object_type: 'merchant', object_id: LIDL.id, category: catAssignment(FOOD) }) }
    start(state, ALL_GRANTS, { ...LIDL, archived: true, archived_at: '2026-11-20T10:00:00Z' })
    await user.click(await screen.findByRole('button', { name: 'Organise Lidl' }))
    const panel = await screen.findByRole('group', { name: 'Organisation of Lidl' })
    expect(await within(panel).findByText(/This merchant is archived: assignments can be read but not changed/)).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Set category' })).toBeNull()
  })

  it('has no Organise control on a merged merchant', async () => {
    start({ a: assignments() }, ALL_GRANTS, { ...LIDL, archived: true, merged_into: 'x' })
    await screen.findByRole('heading', { name: /Lidl/, level: 1 })
    expect(screen.queryByRole('button', { name: 'Organise Lidl' })).toBeNull()
  })
})
