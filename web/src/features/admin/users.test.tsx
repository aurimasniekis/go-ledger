import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, signedIn, token, user as mkUser } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const ALICE = mkUser()
const BOB = mkUser({ id: '01JD3Y4C8V9M2Q7W5R6T0XZBOB', username: 'bob', role: 'read-only' })

describe('users', () => {
  it('lists users and creates one, field-associating server errors', async () => {
    const user = userEvent.setup()
    let n = 0
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/users': ok({ items: [ALICE, BOB], next_cursor: null }),
      'POST /api/v1/users': () =>
        ++n === 1
          ? fail(400, 'validation_failed', 'invalid', { field: 'username', errors: [{ field: 'username', message: 'username may not contain upper-case letters' }] })
          : ok(mkUser({ id: 'x', username: 'carol' }), 201),
    })
    renderApp('/admin/users')
    expect(await screen.findByRole('list', { name: 'Users' })).toHaveTextContent('bob')
    await user.click(screen.getByRole('button', { name: 'Add user' }))
    const dlg = await screen.findByRole('dialog', { name: 'Add a user' })
    await user.type(within(dlg).getByLabelText('Username'), 'Carol{Enter}')
    expect(await within(dlg).findByText('username may not contain upper-case letters')).toBeInTheDocument()
    expect(within(dlg).getByLabelText('Username')).toBeInvalid()
    await user.clear(within(dlg).getByLabelText('Username'))
    await user.type(within(dlg).getByLabelText('Username'), 'carol')
    await user.selectOptions(within(dlg).getByLabelText('Role'), 'administrator')
    await user.click(within(dlg).getByRole('button', { name: 'Add user' }))
    expect(await screen.findByText(/carol was added/)).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'POST').at(-1)!.body).toEqual({ username: 'carol', role: 'administrator' })
  })

  it('disables a user without a confirmation and offers Undo, which re-enables', async () => {
    const user = userEvent.setup()
    let disabled = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/users': () => ok({ items: [ALICE, { ...BOB, disabled, version: disabled ? 2 : 1 }], next_cursor: null }),
      ['PATCH /api/v1/users/' + BOB.id]: (r) => {
        disabled = (r.body as { disabled: boolean }).disabled
        return ok({ ...BOB, disabled, version: disabled ? 2 : 3 })
      },
    })
    renderApp('/admin/users')
    await user.click(await screen.findByRole('button', { name: /Disable\s+bob/ }))
    expect(await screen.findByText(/bob is disabled/)).toBeInTheDocument()
    const patch = calls.find((c) => c.method === 'PATCH')!
    expect(patch.headers['If-Match']).toBe('"1"')
    expect(patch.headers['Content-Type']).toBe('application/merge-patch+json')
    expect(patch.body).toEqual({ disabled: true })
    await user.click(screen.getByRole('button', { name: 'Undo' }))
    expect(await screen.findByText(/bob is enabled again/)).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'PATCH')[1]!.headers['If-Match']).toBe('"2"')
  })

  it('says so when someone else changed the user first', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/users': ok({ items: [ALICE, BOB], next_cursor: null }),
      ['PATCH /api/v1/users/' + BOB.id]: fail(409, 'version_conflict', 'stale', { reconciliation: { current_version: 5 } }),
    })
    renderApp('/admin/users')
    await user.click(await screen.findByRole('button', { name: /Disable\s+bob/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('changed elsewhere')
  })

  it('hides write actions without users:write', async () => {
    installApi({ ...signedIn(undefined, ['users:read']), 'GET /api/v1/users': ok({ items: [ALICE], next_cursor: null }) })
    renderApp('/admin/users')
    await screen.findByRole('list', { name: 'Users' })
    expect(screen.queryByRole('button', { name: 'Add user' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Disable/ })).not.toBeInTheDocument()
  })

  it('sets a password with the field error tied to the field', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/users': ok({ items: [ALICE, BOB], next_cursor: null }),
      ['PUT /api/v1/users/' + BOB.id + '/password']: fail(400, 'validation_failed', 'too short', { field: 'password' }),
    })
    renderApp('/admin/users')
    await user.click(await screen.findByRole('button', { name: /Set password\s+for bob/ }))
    const dlg = await screen.findByRole('dialog')
    await user.type(within(dlg).getByLabelText('New password'), 'x{Enter}')
    expect(await within(dlg).findByText('too short')).toBeInTheDocument()
    expect(within(dlg).getByLabelText('New password')).toBeInvalid()
  })
})

describe('roles and all tokens', () => {
  it('lists the built-in roles with their grants', async () => {
    installApi(signedIn())
    renderApp('/admin/roles')
    expect(await screen.findByRole('group', { name: 'read-only' })).toHaveTextContent('commitments:read')
    expect(screen.getByRole('group', { name: 'administrator' })).toHaveTextContent('users:write')
  })

  it('lists every token with its owner and revokes through the admin route', async () => {
    const user = userEvent.setup()
    let revoked = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/users': ok({ items: [ALICE, BOB], next_cursor: null }),
      'GET /api/v1/tokens': (r) => {
        expect(r.query.get('owner_id') ?? '').toBe('')
        return ok({ items: [token({ owner_id: BOB.id, status: revoked ? 'revoked' : 'active' })], next_cursor: null })
      },
      'DELETE /api/v1/tokens/01JD3Y4C8V9M2Q7W5R6T0XZT01': () => {
        revoked = true
        return { status: 204 }
      },
    })
    renderApp('/admin/tokens')
    const table = await screen.findByRole('table', { name: 'All API tokens' })
    expect(await within(table).findByText('bob')).toBeInTheDocument()
    await user.click(within(table).getByRole('button', { name: /Revoke/ }))
    expect(await screen.findByRole('alertdialog')).toHaveAccessibleDescription(/whoever owns it \(bob\)/)
    await user.click(screen.getByRole('button', { name: 'Revoke token' }))
    expect(await screen.findByText('Revoked')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'DELETE')).toBe(true)
  })
})
