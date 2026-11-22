import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setCsrfToken } from '@/lib/api'
import { makeQueryClient } from '@/lib/query'
import { ALL_GRANTS, fail, installApi, ok, renderApp, signedIn, type Handler, type Req } from '@/test/fake-api'
import { alert, def, suggestion } from './fixtures'
import type { Alert, Suggestion } from './types'

let calls: Req[]
let sugs: Suggestion[]
let alerts: Alert[]
let extra: Record<string, Handler>
let grants: string[]

const routes = (): Record<string, Handler> => ({
  ...signedIn(undefined, grants),
  'GET /api/v1/suggestions': (r) => {
    const st = r.query.getAll('state')
    return ok({ items: sugs.filter((s) => st.includes(s.state)), next_cursor: null })
  },
  'GET /api/v1/alerts': (r) => {
    const st = r.query.getAll('state')
    return ok({ items: alerts.filter((a) => st.includes(a.state)), next_cursor: null })
  },
  'GET /api/v1/categories': ok({ items: [def('cat-groc', 'Groceries', 'category'), def('cat-home', 'Home', 'category')], next_cursor: null }),
  'GET /api/v1/labels': ok({ items: [def('lab-a', 'Essential', 'label'), def('lab-b', 'Shared', 'label')], next_cursor: null }),
  'GET /api/v1/commitments/c-a': ok({ id: 'c-a', name: 'Rent' }),
  ...extra,
})
const start = (path = '/suggestions') => {
  calls = installApi(routes())
  return renderApp(path, makeQueryClient())
}
const posts = () => calls.filter((c) => c.method === 'POST')
const T = { timeout: 5000 }

beforeEach(() => {
  sugs = [suggestion('a')]
  alerts = []
  extra = {}
  grants = [...ALL_GRANTS, 'decisions:read', 'decisions:write']
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  setCsrfToken(null)
})

describe('suggestions review', () => {
  it('shows a pending suggestion with its provenance, the named choice and the state in words', async () => {
    start()
    const card = await screen.findByRole('article', { name: /Category suggestion for/ }, T)
    await waitFor(() => expect(card).toHaveTextContent('Groceries'), T)
    expect(within(card).getByTestId('provenance')).toHaveTextContent('suggested by testprov/test-model')
    expect(card).toHaveTextContent('Needs review')
    expect(card).toHaveTextContent('a proposal')
    expect(card).toHaveTextContent('high')
  })

  it('accepts: posts once, says what was assigned and that the provenance stays', async () => {
    const user = userEvent.setup()
    extra['POST /api/v1/suggestions/sg-a/accept'] = () => {
      sugs = [suggestion('a', { state: 'accepted', decided_at: '2026-11-21T10:00:00Z' })]
      return ok(sugs[0])
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Accept' }, T))
    const note = await screen.findByText(/Accepted: category Groceries/, undefined, T)
    expect(note).toHaveTextContent('suggested by testprov/test-model')
    expect(posts()).toHaveLength(1)
    await waitFor(() => expect(screen.getByText(/No suggestions are waiting/)).toBeInTheDocument())
  })

  it('rejects without assigning anything', async () => {
    const user = userEvent.setup()
    extra['POST /api/v1/suggestions/sg-a/reject'] = () => {
      sugs = []
      return ok(suggestion('a', { state: 'rejected' }))
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Reject' }, T))
    expect(await screen.findByText(/Rejected: nothing was assigned/, undefined, T)).toBeInTheDocument()
  })

  it('corrects with the person\'s own category, sending its id', async () => {
    const user = userEvent.setup()
    extra['POST /api/v1/suggestions/sg-a/correct'] = () => {
      sugs = []
      return ok(suggestion('a', { state: 'rejected' }))
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Correct…' }, T))
    await user.selectOptions(await screen.findByLabelText('Your category'), 'cat-home')
    await user.click(screen.getByRole('button', { name: 'Save my answer' }))
    expect(await screen.findByText(/Corrected: your own answer/, undefined, T)).toBeInTheDocument()
    expect(posts()[0]!.body).toEqual({ category_id: 'cat-home' })
  })

  it('corrects a label suggestion with label ids and needs at least one', async () => {
    const user = userEvent.setup()
    sugs = [suggestion('l', { task: 'label_suggestion', decision: { choice: null, choices: ['lab-a'], confidence: null } })]
    extra['POST /api/v1/suggestions/sg-l/correct'] = () => ok(suggestion('l', { state: 'rejected' }))
    start()
    await user.click(await screen.findByRole('button', { name: 'Correct…' }, T))
    expect(screen.getByRole('button', { name: 'Save my answer' })).toBeDisabled()
    await user.click(screen.getByLabelText('Shared'))
    await user.click(screen.getByRole('button', { name: 'Save my answer' }))
    await screen.findByText(/Corrected/, undefined, T)
    expect(posts()[0]!.body).toEqual({ label_ids: ['lab-b'] })
  })

  it('explains a refusal, keeps the card and reloads (suggestion_not_pending)', async () => {
    const user = userEvent.setup()
    extra['POST /api/v1/suggestions/sg-a/accept'] = () => fail(409, 'suggestion_not_pending', 'x')
    start()
    await user.click(await screen.findByRole('button', { name: 'Accept' }, T))
    expect(await screen.findByRole('alert', undefined, T)).toHaveTextContent('already decided')
    expect(screen.queryByText(/Accepted:/)).not.toBeInTheDocument()
  })

  it('keeps the explanation and its focus when the reload removes the card (the server flipped to accepted)', async () => {
    const user = userEvent.setup()
    extra['POST /api/v1/suggestions/sg-a/accept'] = () => {
      sugs = [suggestion('a', { state: 'accepted', decided_at: '2026-11-21T10:00:00Z' })]
      return fail(409, 'suggestion_not_pending', 'x')
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Accept' }, T))
    const note = await screen.findByRole('alert', undefined, T)
    expect(note).toHaveTextContent('already decided')
    await waitFor(() => expect(screen.queryByRole('article')).not.toBeInTheDocument(), T)
    expect(screen.getByRole('alert')).toHaveTextContent('already decided')
    expect(note).toHaveFocus()
  })

  it('moves focus into the Correct form and back to Correct… on Cancel', async () => {
    const user = userEvent.setup()
    start()
    await user.click(await screen.findByRole('button', { name: 'Correct…' }, T))
    expect(await screen.findByLabelText('Your category')).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Correct…' })).toHaveFocus())
  })

  it('says before the click that a decision cannot be undone from here', async () => {
    start()
    const card = await screen.findByRole('article', { name: /Category suggestion for/ }, T)
    expect(card).toHaveTextContent('cannot be undone from here')
    expect(within(card).getByRole('heading', { level: 2 })).toBeInTheDocument()
  })

  it('offers only reject without the organisation write grant', async () => {
    grants = ['decisions:read', 'decisions:write']
    start()
    await screen.findByRole('button', { name: 'Reject' }, T)
    expect(screen.queryByRole('button', { name: 'Accept' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Correct…' })).not.toBeInTheDocument()
    expect(screen.getByText(/also needs the organisation write grant/)).toBeInTheDocument()
  })

  it('shows decided suggestions, offers Revert for an auto-applied one, and marks reverted ones', async () => {
    const user = userEvent.setup()
    sugs = [suggestion('b', { state: 'accepted', auto_apply: true, decided_at: '2026-11-21T10:00:00Z' }), suggestion('c', { state: 'reverted', auto_apply: true, reversal: { by: { kind: 'user', user_id: 'u' }, at: '2026-11-22T10:00:00Z' } }), suggestion('d', { state: 'superseded' })]
    extra['POST /api/v1/suggestions/sg-b/revert'] = () => ok(suggestion('b', { state: 'reverted', auto_apply: true }))
    start()
    await user.click(await screen.findByRole('button', { name: 'Decided' }, T))
    const items = await screen.findAllByRole('article', undefined, T)
    expect(items).toHaveLength(3)
    expect(screen.getAllByText('Reverted').length).toBeGreaterThan(0)
    expect(screen.getByText('Superseded by a newer suggestion')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Revert automatic assignment/ })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: /Revert automatic assignment/ }))
    expect(await screen.findByText(/Reverted: the automatic assignment/, undefined, T)).toBeInTheDocument()
  })

  it('shows a failed load with a retry, never an empty list', async () => {
    extra['GET /api/v1/suggestions'] = fail(500, 'internal', 'boom')
    start()
    expect(await screen.findByText(/Could not load suggestions/, undefined, T)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })
})

describe('alerts', () => {
  it('lists an open alert with Ledger\'s message, exact facts and labelled model wording, and dismisses it', async () => {
    const user = userEvent.setup()
    alerts = [alert('a', { model_text: { text: 'Worth checking the landlord.', provider: 'testprov', model: 'test-model', label: 'Test', provenance: 'worded by testprov/test-model' } })]
    extra['POST /api/v1/alerts/al-a/dismiss'] = () => {
      alerts = [alert('a', { state: 'dismissed', closed_at: '2026-11-22T10:00:00Z' })]
      return ok(alerts[0])
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Open alerts' }, T))
    const card = await screen.findByRole('article', { name: /Overdue with no matched payment/ }, T)
    expect(card).toHaveTextContent('Rent is 4 days overdue')
    expect(card).toHaveTextContent('120.00 EUR')
    expect(within(card).getByTestId('model-text')).toHaveTextContent('worded by testprov/test-model')
    await user.click(within(card).getByRole('button', { name: 'Dismiss' }))
    expect(await screen.findByText(/Dismissed: "Overdue with no matched payment"/, undefined, T)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Closed alerts' }))
    expect(await screen.findByText('Dismissed', { selector: 'span' }, T)).toBeInTheDocument()
  })

  it('explains a refused dismissal on the page after the closed alert leaves the open list', async () => {
    const user = userEvent.setup()
    alerts = [alert('a')]
    extra['POST /api/v1/alerts/al-a/dismiss'] = () => {
      alerts = [alert('a', { state: 'dismissed', closed_at: '2026-11-22T10:00:00Z' })]
      return fail(409, 'alert_not_open', 'x')
    }
    start()
    await user.click(await screen.findByRole('button', { name: 'Open alerts' }, T))
    await user.click(await screen.findByRole('button', { name: 'Dismiss' }, T))
    expect(await screen.findByRole('alert', undefined, T)).toHaveTextContent('already closed')
    await waitFor(() => expect(screen.queryByRole('article')).not.toBeInTheDocument(), T)
    expect(screen.getByRole('alert')).toHaveTextContent('already closed')
  })

  it('hides Dismiss without decisions:write', async () => {
    grants = ['decisions:read']
    alerts = [alert('a')]
    const user = userEvent.setup()
    start()
    await user.click(await screen.findByRole('button', { name: 'Open alerts' }, T))
    await screen.findByRole('article', undefined, T)
    expect(screen.queryByRole('button', { name: 'Dismiss' })).not.toBeInTheDocument()
  })
})

describe('navigation', () => {
  it('shows the Suggestions link only with decisions:read', async () => {
    grants = ['commitments:read']
    start('/month')
    await screen.findByRole('link', { name: 'Commitments' }, T)
    expect(screen.queryByRole('link', { name: 'Suggestions' })).not.toBeInTheDocument()
  })
})
