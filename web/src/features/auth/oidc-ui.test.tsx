import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch, setCsrfToken } from '@/lib/api'
import { navigation } from '@/lib/oidc'
import { ReauthProvider } from '@/features/auth/reauth'
import { fail, installApi, none, ok, renderApp, session, signedIn } from '@/test/fake-api'

const signedOut = { 'GET /api/v1/auth/session': fail(401, 'unauthenticated', 'sign in required') }
const sso = { password: { enabled: true }, oidc: { enabled: true, name: 'Test IdP', authorize_url: '/api/v1/auth/oidc/authorize' } }
const ssoOnly = { password: { enabled: false }, oidc: { enabled: true, name: 'Test IdP', authorize_url: '/api/v1/auth/oidc/authorize' } }
let goTo: ReturnType<typeof vi.fn>

beforeEach(() => {
  goTo = vi.fn()
  vi.spyOn(navigation, 'goTo').mockImplementation(goTo as (u: string) => void)
})
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  setCsrfToken(null)
  window.history.replaceState(null, '', '/')
})

describe('sign-in with single sign-on', () => {
  it('offers the provider next to the password form and starts the flow', async () => {
    const user = userEvent.setup()
    installApi({ ...signedOut, 'GET /api/v1/auth/methods': ok(sso) })
    renderApp('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in with Test IdP' }))
    expect(goTo).toHaveBeenCalledWith('/api/v1/auth/oidc/authorize?return_to=%2F')
    expect(screen.getByLabelText('Username')).toBeInTheDocument()
  })

  it('shows only the provider when passwords are off', async () => {
    installApi({ ...signedOut, 'GET /api/v1/auth/methods': ok(ssoOnly) })
    renderApp('/sign-in')
    expect(await screen.findByRole('button', { name: 'Sign in with Test IdP' })).toBeInTheDocument()
    expect(screen.queryByLabelText('Username')).not.toBeInTheDocument()
  })

  it('keeps the password form when the methods cannot be read', async () => {
    installApi({ ...signedOut, 'GET /api/v1/auth/methods': fail(500, 'internal', 'boom') })
    renderApp('/sign-in')
    expect(await screen.findByLabelText('Username')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Sign in with/ })).not.toBeInTheDocument()
  })

  it.each([
    ['rate_limited', /Too many sign-in attempts/],
    ['not_linked', /No Ledger account is linked/],
    ['invalid_state', /expired or was not started here/],
    ['something_new', /Single sign-on failed/],
  ])('shows oidc_error=%s as an alert', async (code, text) => {
    installApi({ ...signedOut, 'GET /api/v1/auth/methods': ok(sso) })
    renderApp(`/sign-in?oidc_error=${code}`)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(text)
    expect(alert).not.toHaveTextContent(code)
  })

  it('shows recovery_error and never the raw code', async () => {
    installApi({ ...signedOut, 'GET /api/v1/auth/methods': ok(sso) })
    renderApp('/sign-in?recovery_error=invalid_or_expired')
    expect(await screen.findByRole('alert')).toHaveTextContent('invalid, expired or already used')
  })

  it('takes the second step after the provider from the #second_factor fragment, and clears it', async () => {
    const user = userEvent.setup()
    window.history.replaceState(null, '', '/sign-in?step=second_factor#second_factor=chal%2Btoken')
    const calls = installApi({
      ...signedOut,
      'GET /api/v1/auth/methods': ok(sso),
      'POST /api/v1/auth/sign-in/second-factor': ok(session()),
    })
    renderApp('/sign-in')
    const code = await screen.findByLabelText('Authenticator code')
    expect(window.location.hash).toBe('')
    expect(window.location.search).toBe('')
    await user.type(code, '123456{Enter}')
    await waitFor(() => expect(calls.some((c) => c.method === 'POST')).toBe(true))
    expect(calls.find((c) => c.method === 'POST')!.body).toEqual({ challenge: 'chal+token', code: '123456' })
  })

  it('asks the server for the session on load, so a cookie set by the callback is seen', async () => {
    const calls = installApi({ ...signedIn(), 'GET /api/v1/auth/methods': ok(sso) })
    renderApp('/sign-in')
    expect(await screen.findByRole('heading', { level: 1, name: 'Monthly view' })).toBeInTheDocument()
    expect(calls.some((c) => c.path === '/api/v1/auth/session')).toBe(true)
  })
})

describe('account security: single sign-on link', () => {
  const link = (over = {}) => ({ configured: true, linked: false, provider: 'Test IdP', subject: null, email: null, created_at: null, last_used_at: null, ...over })
  const base = (l: object) => ({
    ...signedIn(),
    'GET /api/v1/auth/two-factor': ok({ enabled: false, method: null, enabled_at: null, recovery_codes_remaining: 0, required: false, pending_enrollment_until: null }),
    'GET /api/v1/auth/oidc/link': ok(l),
  })

  it('starts a link and returns to the security page', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...base(link()),
      'POST /api/v1/auth/oidc/link': ok({ authorization_url: 'https://idp.example.com/authorize?x=1' }),
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Link single sign-on' }))
    await waitFor(() => expect(goTo).toHaveBeenCalledWith('https://idp.example.com/authorize?x=1'))
    expect(calls.find((c) => c.method === 'POST' && c.path.endsWith('/oidc/link'))!.body).toEqual({ return_to: '/account/security' })
  })

  it('confirms oidc=linked and shows who is linked', async () => {
    installApi(base(link({ linked: true, email: 'a@example.com', created_at: '2026-11-21T10:00:00Z' })))
    renderApp('/account/security?oidc=linked')
    expect(await screen.findByText('Your single-sign-on account is linked.')).toBeInTheDocument()
    expect(await screen.findByText(/a@example.com/)).toBeInTheDocument()
  })

  it('shows an oidc_error outcome as an alert', async () => {
    installApi(base(link()))
    renderApp('/account/security?oidc_error=already_linked')
    expect(await screen.findByRole('alert')).toHaveTextContent('already linked')
  })

  it('hides the section when single sign-on is not configured', async () => {
    installApi(base(link({ configured: false })))
    renderApp('/account/security')
    await screen.findByRole('heading', { name: 'Change password' })
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Single sign-on' })).not.toBeInTheDocument())
  })

  it('unlinks after confirmation', async () => {
    const user = userEvent.setup()
    const calls = installApi({ ...base(link({ linked: true, created_at: '2026-11-21T10:00:00Z' })), 'DELETE /api/v1/auth/oidc/link': none() })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Unlink' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Unlink' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE')).toBe(true))
    await waitFor(() => expect(window.location.search).toBe(''))
  })

  it('drops the stale linked confirmation after unlinking', async () => {
    const user = userEvent.setup()
    installApi({ ...base(link({ linked: true, created_at: '2026-11-21T10:00:00Z' })), 'DELETE /api/v1/auth/oidc/link': none() })
    renderApp('/account/security?oidc=linked')
    expect(await screen.findByText('Your single-sign-on account is linked.')).toBeInTheDocument()
    await user.click(await screen.findByRole('button', { name: 'Unlink' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Unlink' }))
    await waitFor(() => expect(screen.queryByText('Your single-sign-on account is linked.')).not.toBeInTheDocument())
  })
})

describe('re-authentication prompt', () => {
  function mount(methods: object) {
    const calls = installApi({
      'GET /api/v1/auth/methods': ok(methods),
      'POST /api/v1/thing': fail(403, 'reauthentication_required', 'confirm'),
      'POST /api/v1/auth/oidc/reauthenticate': ok({ authorization_url: 'https://idp.example.com/authorize?r=1' }),
    })
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ReauthProvider>
          <p>page</p>
        </ReauthProvider>
      </QueryClientProvider>,
    )
    const pending = apiFetch('/api/v1/thing', { method: 'POST' }).catch((e: unknown) => e)
    return { calls, pending }
  }

  it('re-authenticates at the provider when passwords are off', async () => {
    const user = userEvent.setup()
    window.history.replaceState(null, '', '/account/tokens?x=1')
    const { calls } = mount(ssoOnly)
    await screen.findByRole('dialog', { name: 'Confirm with your identity provider' })
    expect(screen.queryByLabelText('Password')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Continue to your provider' }))
    await waitFor(() => expect(goTo).toHaveBeenCalledWith('https://idp.example.com/authorize?r=1'))
    expect(calls.find((c) => c.path.endsWith('/oidc/reauthenticate'))!.body).toEqual({ return_to: '/account/tokens?x=1' })
  })

  it('cancelling fails the waiting request with the server message', async () => {
    const user = userEvent.setup()
    const { pending } = mount(ssoOnly)
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    const out = await pending
    expect(out).toBeInstanceOf(ApiError)
  })

  it('keeps the password prompt when passwords are on', async () => {
    mount(sso)
    expect(await screen.findByRole('dialog', { name: 'Confirm your password' })).toBeInTheDocument()
  })
})
