import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, none, ok, renderApp, sess, session, signedIn } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const A = '01JD3Y4C8V9M2Q7W5R6T0XZB01'
const B = '01JD3Y4C8V9M2Q7W5R6T0XZB02'

describe('sessions', () => {
  it('lists sessions, marks the current one and ends another after confirmation', async () => {
    const user = userEvent.setup()
    let ended = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/sessions': () => ok({ items: [sess({ id: A, current: true }), ...(ended ? [] : [sess({ id: B, user_agent: 'Other/2', address: '198.51.100.7' })])] }),
      ['DELETE /api/v1/auth/sessions/' + B]: () => {
        ended = true
        return none()
      },
    })
    renderApp('/account/sessions')
    expect(await screen.findByText('This session')).toBeInTheDocument()
    expect(screen.getByText('Other/2')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /End session started/ }))
    const dlg = await screen.findByRole('alertdialog', { name: 'End this session?' })
    await user.click(within(dlg).getByRole('button', { name: 'End session' }))
    expect(await screen.findByRole('heading', { name: 'Sessions' })).toBeInTheDocument()
    await vi.waitFor(() => expect(screen.queryByText('Other/2')).not.toBeInTheDocument())
    expect(calls.find((c) => c.method === 'DELETE')!.headers['X-CSRF-Token']).toBe('csrf-abc')
  })

  it('ending the current session signs out and goes to sign-in', async () => {
    const user = userEvent.setup()
    let out = false
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/session': () => (out ? fail(401, 'unauthenticated', 'sign in') : ok(session())),
      'GET /api/v1/auth/sessions': ok({ items: [sess({ id: A, current: true })] }),
      ['DELETE /api/v1/auth/sessions/' + A]: () => {
        out = true
        return none()
      },
    })
    renderApp('/account/sessions')
    await user.click(await screen.findByRole('button', { name: 'Sign out of this session' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Sign out' }))
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('shows a failed end-session without removing the row', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/sessions': ok({ items: [sess({ id: B, user_agent: 'Other/2' })] }),
      ['DELETE /api/v1/auth/sessions/' + B]: fail(404, 'not_found', 'no such session'),
    })
    renderApp('/account/sessions')
    await user.click(await screen.findByRole('button', { name: /End session started/ }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'End session' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('no such session')
    expect(screen.getByText('Other/2')).toBeInTheDocument()
  })
})
