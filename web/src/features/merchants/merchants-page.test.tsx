import { QueryClient } from '@tanstack/react-query'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { ALL_GRANTS, fail, installApi, none, ok, renderApp, signedIn } from '@/test/fake-api'
import { alias, merchant } from './fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const LIDL = merchant('L00', 'Lidl', { aliases: [alias('L01', 'Lidl'), alias('L02', 'Lidl Vilnius', { origin: 'decision' })] })
const MAXIMA = merchant('M00', 'Maxima')
const OLD = merchant('O00', 'Old shop', { archived: true, archived_at: '2026-11-20T10:00:00Z' })
const page = (items: unknown[]) => ok({ items, next_cursor: null })
const etag = (m: { version: number }) => ({ 'If-Match': `"${m.version}"` })

function routes(extra: Parameters<typeof installApi>[0] = {}, grants = ALL_GRANTS) {
  return { ...signedIn(undefined, grants), 'GET /api/v1/merchants': page([LIDL, MAXIMA]), ...extra }
}

describe('merchants list', () => {
  it('lists active merchants with their other aliases, linking to the record, and asks for archived ones only on request', async () => {
    const user = userEvent.setup()
    const calls = installApi(routes())
    renderApp('/merchants')
    const list = await screen.findByRole('list', { name: 'merchants' })
    expect(within(list).getByRole('link', { name: 'Lidl' })).toHaveAttribute('href', '/merchants/' + LIDL.id)
    expect(within(list).getByText('Also matched as: Lidl Vilnius')).toBeInTheDocument()
    expect(calls.find((c) => c.path === '/api/v1/merchants')!.query.get('archived')).toBeNull()
    await user.click(screen.getByLabelText('Show archived and merged merchants'))
    await vi.waitFor(() => expect(calls.some((c) => c.path === '/api/v1/merchants' && c.query.get('archived') === 'true')).toBe(true))
  })

  it('filters by an alias', async () => {
    const user = userEvent.setup()
    installApi(routes())
    renderApp('/merchants')
    await screen.findByRole('list', { name: 'merchants' })
    await user.type(screen.getByLabelText('Find a merchant by name or alias'), 'vilnius')
    const list = screen.getByRole('list', { name: 'merchants' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(1)
    expect(within(list).queryByText('Maxima')).toBeNull()
  })

  it('finds a name typed with different case, spacing or composition of accents, by name or alias', async () => {
    const user = userEvent.setup()
    const SKODA = merchant('S00', 'Škoda  Auto', { aliases: [alias('S01', 'Škoda Auto')] })
    installApi(routes({ 'GET /api/v1/merchants': page([LIDL, SKODA]) }))
    renderApp('/merchants')
    await screen.findByRole('list', { name: 'merchants' })
    const find = screen.getByLabelText('Find a merchant by name or alias')
    for (const q of ['ŠKODA', 'S\u{30C}koda  auto', 'škoda']) {
      await user.clear(find)
      await user.type(find, q)
      const list = screen.getByRole('list', { name: 'merchants' })
      expect(within(list).getAllByRole('listitem')).toHaveLength(1)
      expect(within(list).getByRole('link', { name: /Škoda/ })).toBeInTheDocument()
    }
  })

  it('offers no change without merchants:write, and no list without merchants:read', async () => {
    installApi(routes({}, ['merchants:read']))
    const { unmount } = renderApp('/merchants')
    await screen.findByRole('list', { name: 'merchants' })
    expect(screen.queryByRole('button', { name: 'Add merchant' })).toBeNull()
    expect(screen.getByText(/does not hold merchants:write/)).toBeInTheDocument()
    unmount()
    installApi(routes({}, ['commitments:read']))
    renderApp('/merchants')
    expect(await screen.findByText(/does not hold merchants:read/)).toBeInTheDocument()
  })

  it('says so when Ledger cannot be reached', async () => {
    installApi({
      ...signedIn(),
      'GET /api/v1/merchants': () => {
        throw new TypeError('offline')
      },
    })
    renderApp('/merchants', new QueryClient({ defaultOptions: { queries: { retry: false } } }))
    expect(await screen.findByText(/no connection to the server/)).toBeInTheDocument()
  })

  it('shows a name another merchant already has against the field, and keeps the dialog open', async () => {
    const user = userEvent.setup()
    installApi(routes({ 'POST /api/v1/merchants': fail(409, 'merchant_alias_taken', 'the alias belongs to another merchant') }))
    renderApp('/merchants')
    await user.click(await screen.findByRole('button', { name: 'Add merchant' }))
    const dlg = await screen.findByRole('dialog', { name: 'Add a merchant' })
    await user.type(within(dlg).getByLabelText('Display name'), 'Lidl{Enter}')
    expect(await within(dlg).findByText(/Another merchant already has this name/)).toBeInTheDocument()
    expect(screen.getByRole('dialog', { name: 'Add a merchant' })).toBeInTheDocument()
  })
})

describe('merchant record', () => {
  const rec = (over = {}) => ({ ...LIDL, ...over })

  it('renames with If-Match and shows the result only after the server accepted it', async () => {
    const user = userEvent.setup()
    let current = LIDL
    const calls = installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: () => ok(current),
        ['PATCH /api/v1/merchants/' + LIDL.id]: (req) => {
          expect(req.headers['If-Match']).toBe('"1"')
          current = rec({ display_name: 'Lidl Lietuva', version: 2 })
          return ok(current)
        },
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await screen.findByRole('heading', { name: 'Lidl', level: 1 })
    await user.click(screen.getByRole('button', { name: 'Rename' }))
    const dlg = await screen.findByRole('dialog', { name: 'Rename Lidl' })
    const input = within(dlg).getByLabelText('Display name')
    await user.clear(input)
    await user.type(input, 'Lidl Lietuva{Enter}')
    expect(await screen.findByText('Renamed to Lidl Lietuva.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: /Lidl Lietuva/, level: 1 })).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PATCH')!.body).toEqual({ display_name: 'Lidl Lietuva' })
  })

  it('reloads and says nothing changed on a version conflict', async () => {
    const user = userEvent.setup()
    let reads = 0
    installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: () => (++reads, ok(reads > 1 ? rec({ version: 5, display_name: 'Lidl (renamed elsewhere)' }) : LIDL)),
        ['PATCH /api/v1/merchants/' + LIDL.id]: fail(409, 'version_conflict', 'stale', { reconciliation: { current_version: 5 } }),
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    const dlg = await screen.findByRole('dialog')
    await user.type(within(dlg).getByLabelText('Display name'), ' x{Enter}')
    expect(await within(dlg).findByText(/changed elsewhere.*Nothing was changed/)).toBeInTheDocument()
    // The page behind the open dialog is reloaded (it is inert, hence hidden: true).
    expect(await screen.findByRole('heading', { name: /renamed elsewhere/, level: 1, hidden: true })).toBeInTheDocument()
  })

  it('adds an alias, and shows merchant_alias_taken without adding it', async () => {
    const user = userEvent.setup()
    let n = 0
    let current = LIDL
    installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: () => ok(current),
        ['POST /api/v1/merchants/' + LIDL.id + '/aliases']: (req) => {
          if (++n === 1) return fail(409, 'merchant_alias_taken', 'taken')
          current = rec({ aliases: [...LIDL.aliases, alias('L03', (req.body as { name: string }).name)] })
          return ok(current)
        },
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    const input = await screen.findByLabelText('Add an alias')
    await user.type(input, 'Maxima{Enter}')
    expect(await screen.findByText(/Another merchant already has this name or alias/)).toBeInTheDocument()
    await user.clear(input)
    await user.type(input, 'Lidl Kaunas{Enter}')
    expect(await screen.findByText('The alias Lidl Kaunas was added.')).toBeInTheDocument()
    expect(within(screen.getByRole('list', { name: 'aliases' })).getByText('Lidl Kaunas')).toBeInTheDocument()
  })

  it('removes an alias and offers to undo it', async () => {
    const user = userEvent.setup()
    const calls = installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL),
        ['DELETE /api/v1/merchants/' + LIDL.id + '/aliases/' + LIDL.aliases[1]!.id]: ok(rec({ aliases: [LIDL.aliases[0]] })),
        ['POST /api/v1/merchants/' + LIDL.id + '/aliases']: ok(LIDL),
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Remove the alias Lidl Vilnius' }))
    expect(await screen.findByText(/The alias Lidl Vilnius was removed.*Undo adds it back as an alias you added/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    await vi.waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.path.endsWith('/aliases'))?.body).toEqual({ name: 'Lidl Vilnius' }))
  })

  it('moves focus to the notice after a rename, not back to the button that opened the dialog', async () => {
    const user = userEvent.setup()
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL), ['PATCH /api/v1/merchants/' + LIDL.id]: ok(rec({ display_name: 'Lidl Lietuva', version: 2 })) }))
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Rename' }))
    const dlg = await screen.findByRole('dialog', { name: 'Rename Lidl' })
    await user.clear(within(dlg).getByLabelText('Display name'))
    await user.type(within(dlg).getByLabelText('Display name'), 'Lidl Lietuva{Enter}')
    const notice = (await screen.findByText('Renamed to Lidl Lietuva.')).closest('[role="status"]')!
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await vi.waitFor(() => expect(document.activeElement).toBe(notice))
  })

  it('returns focus to the opening button when a dialog is cancelled', async () => {
    const user = userEvent.setup()
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL) }))
    renderApp('/merchants/' + LIDL.id)
    const open = await screen.findByRole('button', { name: 'Rename' })
    await user.click(open)
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Cancel' }))
    await vi.waitFor(() => expect(document.activeElement).toBe(open))
  })

  it('keeps focus on the Saving status while an Undo is pending, then on the new notice', async () => {
    const user = userEvent.setup()
    let release: () => void = () => {}
    const gate = new Promise<void>((r) => (release = r))
    installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL),
        ['POST /api/v1/merchants/' + LIDL.id + '/archive']: ok(rec({ archived: true, archived_at: '2026-11-22T10:00:00Z', version: 2 })),
        ['POST /api/v1/merchants/' + LIDL.id + '/unarchive']: async () => {
          await gate
          return ok(LIDL)
        },
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    const saving = await screen.findByText(/Saving… this change is not saved until the server confirms it/)
    await vi.waitFor(() => expect(document.activeElement).toBe(saving))
    release()
    const notice = (await screen.findByText('Lidl is active again.')).closest('[role="status"]')!
    await vi.waitFor(() => expect(document.activeElement).toBe(notice))
  })

  it('moves focus to the problem when an Undo is refused', async () => {
    const user = userEvent.setup()
    installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL),
        ['POST /api/v1/merchants/' + LIDL.id + '/archive']: ok(rec({ archived: true, archived_at: '2026-11-22T10:00:00Z', version: 2 })),
        ['POST /api/v1/merchants/' + LIDL.id + '/unarchive']: fail(409, 'merchant_archived', 'archived'),
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    await user.click(await screen.findByRole('button', { name: 'Undo' }))
    const problem = await screen.findByText(/Nothing was changed/)
    await vi.waitFor(() => expect(document.activeElement).toBe(problem.closest('[role="alert"]')))
  })

  it('archives with Undo, and only says so after the server answered', async () => {
    const user = userEvent.setup()
    let current = LIDL
    installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: () => ok(current),
        ['POST /api/v1/merchants/' + LIDL.id + '/archive']: (req) => {
          expect(req.headers['If-Match']).toBe('"1"')
          current = rec({ archived: true, archived_at: '2026-11-22T10:00:00Z', version: 2 })
          return ok(current)
        },
        ['POST /api/v1/merchants/' + LIDL.id + '/unarchive']: ok(LIDL),
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Archive' }))
    expect(await screen.findByText(/Lidl is archived\. Everything that refers to it is kept/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Unarchive' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Merge another merchant into this one' })).toBeDisabled()
  })

  it('keeps the merchant and says why when it is in use', async () => {
    const user = userEvent.setup()
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL), ['DELETE /api/v1/merchants/' + LIDL.id]: fail(409, 'merchant_in_use', 'in use') }))
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const dlg = await screen.findByRole('alertdialog')
    await user.click(within(dlg).getByRole('button', { name: 'Delete Lidl' }))
    expect(await within(dlg).findByText(/still refer to this merchant, so it cannot be deleted/)).toBeInTheDocument()
  })

  it('deletes an unused merchant and returns to the list', async () => {
    const user = userEvent.setup()
    const calls = installApi(routes({ ['GET /api/v1/merchants/' + MAXIMA.id]: ok(MAXIMA), ['DELETE /api/v1/merchants/' + MAXIMA.id]: none() }))
    renderApp('/merchants/' + MAXIMA.id)
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete Maxima' }))
    const heading = await screen.findByRole('heading', { name: 'Merchants', level: 1 })
    await vi.waitFor(() => expect(document.activeElement).toBe(heading))
    expect(calls.find((c) => c.method === 'DELETE')!.headers).toMatchObject(etag(MAXIMA))
  })

  it('says the result is unknown after a 500 on delete, never that nothing changed', async () => {
    const user = userEvent.setup()
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL), ['DELETE /api/v1/merchants/' + LIDL.id]: fail(500, 'internal', 'boom') }))
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Delete' }))
    const dlg = await screen.findByRole('alertdialog')
    await user.click(within(dlg).getByRole('button', { name: 'Delete Lidl' }))
    expect(await within(dlg).findByText(/The result is unknown: reload the merchant to check/)).toBeInTheDocument()
    expect(within(dlg).queryByText(/Nothing was changed/)).toBeNull()
  })

  it('focuses the list heading only when arriving with the delete flag, not on a plain visit', async () => {
    installApi(routes({ ['GET /api/v1/merchants']: ok({ items: [LIDL], next_cursor: null }) }))
    renderApp('/merchants')
    const heading = await screen.findByRole('heading', { name: 'Merchants', level: 1 })
    expect(document.activeElement).not.toBe(heading)
  })

  it('previews what a merge moves, needs an acknowledgement that it cannot be undone, then merges with If-Match of the remaining merchant', async () => {
    const user = userEvent.setup()
    const calls = installApi(
      routes({
        ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL),
        ['POST /api/v1/merchants/' + LIDL.id + '/merge']: ok({
          merchant: rec({ aliases: [...LIDL.aliases, ...MAXIMA.aliases], version: 2 }),
          moved: { aliases: 1, decisions: 2, enrichments: 3, categories: 1, labels: 2 },
          kept: 1,
        }),
      }),
    )
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Merge another merchant into this one' }))
    const dlg = await screen.findByRole('dialog', { name: /Merge another merchant into Lidl/ })
    const merge = within(dlg).getByRole('button', { name: 'Merge' })
    expect(merge).toBeDisabled()
    // The merchant being merged is not offered as its own source.
    const select = within(dlg).getByLabelText('Merchant to merge into this one')
    expect(within(select).queryByRole('option', { name: 'Lidl' })).toBeNull()
    await user.selectOptions(select, MAXIMA.id)
    const preview = within(dlg).getByLabelText('What the merge does')
    expect(preview).toHaveTextContent('Maxima will be merged into Lidl, which remains.')
    expect(preview).toHaveTextContent('Its 1 alias move to Lidl: Maxima.')
    expect(preview).toHaveTextContent('A merge cannot be undone in place.')
    expect(merge).toBeDisabled()
    await user.click(within(dlg).getByLabelText('I understand this merge cannot be undone in place.'))
    await user.click(merge)
    expect(await screen.findByText(/Maxima was merged into Lidl and archived\. Moved: 1 alias, 2 decisions, 3 source answers, 1 category and 2 label assignments\..*: 1\. This cannot be undone in place/)).toBeInTheDocument()
    const req = calls.find((c) => c.method === 'POST' && c.path.endsWith('/merge'))!
    expect(req.headers['If-Match']).toBe('"1"')
    expect(req.body).toEqual({ merchant_id: MAXIMA.id })
    await vi.waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await vi.waitFor(() => expect(document.activeElement).toBe(screen.getByText(/Maxima was merged into Lidl/).closest('[role="status"]')))
  })

  it('shows a merge refusal and moves nothing', async () => {
    const user = userEvent.setup()
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(LIDL), ['POST /api/v1/merchants/' + LIDL.id + '/merge']: fail(409, 'merchant_archived', 'archived') }))
    renderApp('/merchants/' + LIDL.id)
    await user.click(await screen.findByRole('button', { name: 'Merge another merchant into this one' }))
    const dlg = await screen.findByRole('dialog')
    await user.selectOptions(within(dlg).getByLabelText('Merchant to merge into this one'), MAXIMA.id)
    await user.click(within(dlg).getByLabelText('I understand this merge cannot be undone in place.'))
    await user.click(within(dlg).getByRole('button', { name: 'Merge' }))
    expect(await within(dlg).findByText(/archived or merged.*Nothing was changed/)).toBeInTheDocument()
  })

  it('shows a merged merchant read-only, pointing at the remaining one', async () => {
    const merged = rec({ archived: true, merged_into: MAXIMA.id })
    installApi(routes({ ['GET /api/v1/merchants/' + LIDL.id]: ok(merged) }))
    renderApp('/merchants/' + LIDL.id)
    expect(await screen.findByText(/was merged into/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'another merchant' })).toHaveAttribute('href', '/merchants/' + MAXIMA.id)
    expect(screen.queryByRole('button', { name: 'Rename' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Remove the alias/ })).toBeNull()
  })

  it('says when there is no such merchant', async () => {
    installApi(routes({ ['GET /api/v1/merchants/' + OLD.id]: fail(404, 'not_found', 'no merchant') }))
    renderApp('/merchants/' + OLD.id)
    expect(await screen.findByRole('heading', { name: 'Merchant not found' })).toBeInTheDocument()
  })
})
