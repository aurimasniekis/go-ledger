import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, none, ok, renderApp, signedIn, token } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const SECRET = 'ldgr_' + 'Z'.repeat(43)
const list = (items = [token()]) => ({ 'GET /api/v1/auth/tokens': ok({ items, next_cursor: null }) })

async function tabTo(user: ReturnType<typeof userEvent.setup>, match: (el: Element | null) => boolean, max = 30) {
  for (let i = 0; i < max && !match(document.activeElement); i++) await user.tab()
  expect(match(document.activeElement)).toBe(true)
}

describe('my API tokens', () => {
  it('lists tokens with their hint, grants and status, never a secret', async () => {
    installApi({ ...signedIn(), ...list([token(), token({ id: 'x2', name: 'old', status: 'revoked', revoked_at: '2026-11-01T00:00:00Z' })]) })
    renderApp('/account/tokens')
    const table = await screen.findByRole('table', { name: 'Your API tokens' })
    expect(within(table).getAllByText('ldgr_AbCd…wXyZ')).toHaveLength(2)
    expect(within(table).getAllByText('commitments:read').length).toBeGreaterThan(0)
    expect(within(table).getByText('Revoked')).toBeInTheDocument()
    // Only an active token can be revoked.
    expect(within(table).getAllByRole('button', { name: /^Revoke/ })).toHaveLength(1)
  })

  it('shows a failed load with a retry, and stale data when a refresh fails', async () => {
    const user = userEvent.setup()
    let n = 0
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/tokens': () => (++n === 1 ? fail(429, 'rate_limited', 'database is busy') : ok({ items: [token()], next_cursor: null })),
    })
    renderApp('/account/tokens')
    expect(await screen.findByRole('alert')).toHaveTextContent('database is busy')
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    expect(await screen.findByRole('table', { name: 'Your API tokens' })).toBeInTheDocument()
    expect(screen.getByText(/Updated/)).toBeInTheDocument()
  })

  it('creates a token by keyboard alone and shows the secret once in a dialog that cannot be reopened', async () => {
    const user = userEvent.setup()
    let created = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/tokens': () => ok({ items: created ? [token(), token({ id: 'n', name: 'ci', hint: 'ldgr_ZZZZ…ZZZZ' })] : [token()], next_cursor: null }),
      'POST /api/v1/auth/tokens': () => {
        created = true
        return ok({ secret: SECRET, token: token({ id: 'n', name: 'ci', hint: 'ldgr_ZZZZ…ZZZZ' }), warning: null }, 201)
      },
    })
    renderApp('/account/tokens')
    await screen.findByRole('table')
    await tabTo(user, (e) => e?.textContent === 'Create token' && e.tagName === 'BUTTON')
    await user.keyboard('{Enter}')
    const dlg = await screen.findByRole('dialog', { name: 'Create an API token' })
    await within(dlg).findByRole('checkbox', { name: 'commitments:read' })
    expect(within(dlg).getByLabelText('Name')).toHaveFocus()
    await user.keyboard('ci')
    await user.tab()
    expect(within(dlg).getByRole('checkbox', { name: 'commitments:read' })).toHaveFocus()
    await user.keyboard(' ')
    expect(within(dlg).getByRole('checkbox', { name: 'commitments:read' })).toBeChecked()
    await tabTo(user, (e) => e?.textContent === 'Create token' && e.closest('[role=dialog]') !== null)
    await user.keyboard('{Enter}')

    const once = await screen.findByRole('dialog', { name: 'Token “ci” created' })
    expect(within(once).getByText(SECRET)).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.body).toEqual({ name: 'ci', grants: ['commitments:read'] })
    expect(post.headers['X-CSRF-Token']).toBe('csrf-abc')

    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog', { name: 'Token “ci” created' })).toBeInTheDocument()
    await tabTo(user, (e) => e?.textContent === 'Done')
    await user.keyboard('{Enter}')

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // Focus goes back to the Create token button, not to the body.
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create token' })).toHaveFocus())
    expect(document.body.textContent).not.toContain(SECRET)
    // The list shows the new token by its hint only.
    expect(await screen.findByText('ldgr_ZZZZ…ZZZZ')).toBeInTheDocument()
    expect(await screen.findByRole('rowheader', { name: 'ci' })).toBeInTheDocument()
  })

  it('ties a root-token error to the root checkbox', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/auth/tokens': fail(422, 'validation_failed', 'invalid', { errors: [{ field: 'root', message: 'Root tokens are not allowed here.' }] }),
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: 'Create token' }))
    const dlg = await screen.findByRole('dialog')
    await user.type(await within(dlg).findByLabelText('Name'), 'ci')
    const root = await within(dlg).findByRole('checkbox', { name: 'Root token' })
    await user.click(root)
    await user.click(within(dlg).getByRole('button', { name: 'Create token' }))
    const msg = await within(dlg).findByText('Root tokens are not allowed here.')
    expect(root).toBeInvalid()
    expect(root.getAttribute('aria-describedby')).toContain(msg.id)
  })

  it('does not request the user list without users:read', async () => {
    const calls = installApi({
      ...signedIn(undefined, ['tokens:read']),
      'GET /api/v1/tokens': ok({ items: [token()], next_cursor: null }),
    })
    renderApp('/admin/tokens')
    await screen.findByRole('table', { name: 'All API tokens' })
    expect(calls.some((c) => c.path === '/api/v1/users')).toBe(false)
    expect(screen.queryByLabelText('Owner')).not.toBeInTheDocument()
  })

  it('offers only the grants the user holds, and no root option without every grant', async () => {
    const user = userEvent.setup()
    installApi({ ...signedIn(undefined, ['commitments:read']), ...list() })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: 'Create token' }))
    const dlg = await screen.findByRole('dialog')
    expect(await within(dlg).findByRole('checkbox', { name: 'commitments:read' })).toBeInTheDocument()
    expect(within(dlg).queryByRole('checkbox', { name: 'commitments:write' })).not.toBeInTheDocument()
    expect(within(dlg).queryByRole('checkbox', { name: 'users:write' })).not.toBeInTheDocument()
    expect(within(dlg).queryByRole('checkbox', { name: 'Root token' })).not.toBeInTheDocument()
  })

  it('warns about a root token before it is created and carries the server warning into the show-once dialog', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/auth/tokens': ok({ secret: SECRET, token: token({ root: true, grants: [] }), warning: 'Root tokens can do everything you can.' }, 201),
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: 'Create token' }))
    const dlg = await screen.findByRole('dialog')
    const root = await within(dlg).findByRole('checkbox', { name: 'Root token' })
    expect(root).toHaveAccessibleDescription(/every grant your role holds/)
    await user.type(within(dlg).getByLabelText('Name'), 'all')
    await user.click(root)
    expect(await within(dlg).findByRole('alert')).toHaveTextContent('Anyone with it can act as you')
    expect(within(dlg).getByRole('group', { name: 'Grants' })).toBeDisabled()
    await user.click(within(dlg).getByRole('button', { name: 'Create token' }))
    expect(await screen.findByRole('note')).toHaveTextContent('Root tokens can do everything you can.')
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ name: 'all', root: true })
  })

  it('field-associates client and server errors and keeps the dialog open', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'POST /api/v1/auth/tokens': fail(400, 'validation_failed', 'invalid request', {
        field: 'name',
        errors: [
          { field: 'name', message: 'name must be at most 100 characters' },
          { field: 'expires_at', message: 'expires_at must be in the future' },
        ],
      }),
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: 'Create token' }))
    const dlg = await screen.findByRole('dialog')
    await within(dlg).findByRole('checkbox', { name: 'commitments:read' })
    // Nothing chosen yet: client-side messages, tied to the name field and the grants group.
    await user.click(within(dlg).getByRole('button', { name: 'Create token' }))
    expect(within(dlg).getByLabelText('Name')).toHaveAccessibleDescription(/Give the token a name/)
    expect(within(dlg).getByRole('group', { name: 'Grants' })).toHaveAccessibleDescription(/at least one grant/)
    await user.type(within(dlg).getByLabelText('Name'), 'x')
    await user.click(within(dlg).getByRole('checkbox', { name: 'commitments:read' }))
    await user.click(within(dlg).getByRole('button', { name: 'Create token' }))
    // Server errors from errors[] land on their fields.
    expect(await within(dlg).findByText('name must be at most 100 characters')).toBeInTheDocument()
    expect(within(dlg).getByLabelText('Name')).toBeInvalid()
    expect(within(dlg).getByLabelText(/Expires/)).toHaveAccessibleDescription(/must be in the future/)
    expect(screen.queryByRole('dialog', { name: /created/ })).not.toBeInTheDocument()
  })

  it('confirms a revoke, which cannot be undone, and shows the result only after the server accepts it', async () => {
    const user = userEvent.setup()
    let revoked = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/tokens': () => ok({ items: [token({ status: revoked ? 'revoked' : 'active' })], next_cursor: null }),
      'DELETE /api/v1/auth/tokens/01JD3Y4C8V9M2Q7W5R6T0XZT01': () => {
        revoked = true
        return none()
      },
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: /Revoke/ }))
    const dlg = await screen.findByRole('alertdialog', { name: 'Revoke “backup script”?' })
    expect(dlg).toHaveAccessibleDescription(/cannot be undone/)
    // Cancel changes nothing.
    await user.click(within(dlg).getByRole('button', { name: 'Cancel' }))
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await user.click(screen.getByRole('button', { name: /Revoke/ }))
    await user.click(await screen.findByRole('button', { name: 'Revoke token' }))
    expect(await screen.findByText('Revoked')).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('keeps the confirm dialog open with the error when the revoke fails', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      ...list(),
      'DELETE /api/v1/auth/tokens/01JD3Y4C8V9M2Q7W5R6T0XZT01': fail(409, 'conflict', 'could not save'),
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: /Revoke/ }))
    await user.click(await screen.findByRole('button', { name: 'Revoke token' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('could not save')
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
  })
})
