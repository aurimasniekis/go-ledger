import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, session, signedIn } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'
import { safeRedirect } from '@/features/auth/sign-in'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
  window.history.replaceState(null, '', '/')
})

const signedOut = { 'GET /api/v1/auth/session': fail(401, 'unauthenticated', 'sign in required') }

describe('safeRedirect', () => {
  it('keeps same-origin paths and rejects everything else', () => {
    expect(safeRedirect('/month?m=2026-11')).toBe('/month?m=2026-11')
    for (const bad of ['//evil.example', '/\\evil.example', '/a\\b', '/a\tb', '/a\nb', '/a\x00b', '/a\x7fb', 'https://evil.example', '/sign-in', undefined, '']) {
      expect(safeRedirect(bad)).toBe('/')
    }
  })
})

describe('the redirect through the second factor', () => {
  const challenge = () => ok({ second_factor_required: true, challenge: 'chal', expires_at: '2026-11-21T10:05:00Z', methods: ['totp'] }, 202)
  const methods = ok({ password: { enabled: true }, oidc: { enabled: false, name: null, authorize_url: null } })

  function mount(path: string) {
    let signed = false
    const calls = installApi({
      'GET /api/v1/auth/session': () => (signed ? ok(session()) : fail(401, 'unauthenticated', 'sign in required')),
      'GET /api/v1/auth/methods': methods,
      'POST /api/v1/auth/sign-in': challenge(),
      'POST /api/v1/auth/sign-in/second-factor': () => {
        signed = true
        return ok(session())
      },
    })
    renderApp(path)
    return calls
  }

  it('returns to the kept address after the password and the second step', async () => {
    const user = userEvent.setup()
    const calls = mount(`/sign-in?redirect=${encodeURIComponent('/account/tokens')}`)
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'pw{Enter}')
    await user.type(await screen.findByLabelText('Authenticator code'), '123456{Enter}')
    expect(await screen.findByRole('heading', { name: 'API tokens' })).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'POST').at(-1)!.body).toEqual({ challenge: 'chal', code: '123456' })
  })

  it('ignores a //evil target after the second step and opens the monthly view', async () => {
    const user = userEvent.setup()
    mount(`/sign-in?redirect=${encodeURIComponent('//evil.example/steal')}`)
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'pw{Enter}')
    await user.type(await screen.findByLabelText('Authenticator code'), '123456{Enter}')
    expect(await screen.findByRole('heading', { level: 1, name: /^\w+ \d{4}$/ })).toBeInTheDocument()
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument()
  })

  it('keeps the redirect through the provider challenge in the fragment (good path)', async () => {
    const user = userEvent.setup()
    const path = `/sign-in?step=second_factor&redirect=${encodeURIComponent('/account/tokens')}#second_factor=chal`
    window.history.replaceState(null, '', path)
    mount(path)
    await user.type(await screen.findByLabelText('Authenticator code'), '123456{Enter}')
    expect(await screen.findByRole('heading', { name: 'API tokens' })).toBeInTheDocument()
  })

  it('ignores a //evil target carried through the provider challenge', async () => {
    const user = userEvent.setup()
    const path = `/sign-in?step=second_factor&redirect=${encodeURIComponent('//evil.example/steal')}#second_factor=chal`
    window.history.replaceState(null, '', path)
    mount(path)
    await user.type(await screen.findByLabelText('Authenticator code'), '123456{Enter}')
    expect(await screen.findByRole('heading', { level: 1, name: /^\w+ \d{4}$/ })).toBeInTheDocument()
    expect(screen.queryByText('Page not found')).not.toBeInTheDocument()
  })
})

describe('sign-in', () => {
  it('sends a signed-out visitor to sign-in, remembering where they were going', async () => {
    installApi(signedOut)
    renderApp('/account/tokens')
    expect(await screen.findByRole('heading', { name: 'Sign in' })).toBeInTheDocument()
  })

  it('works by keyboard alone: type, Tab, type, Enter', async () => {
    const user = userEvent.setup()
    let signed = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/session': () => (signed ? ok(session()) : fail(401, 'unauthenticated', 'sign in required')),
      'POST /api/v1/auth/sign-in': () => {
        signed = true
        return ok(session())
      },
    })
    renderApp('/sign-in')
    const username = await screen.findByLabelText('Username')
    expect(username).toHaveFocus() // the first field is focused on arrival
    await user.keyboard('alice')
    await user.tab()
    expect(screen.getByLabelText('Password')).toHaveFocus()
    await user.keyboard('correct horse battery{Enter}')
    expect(await screen.findByRole('heading', { level: 1, name: /^\w+ \d{4}$/ })).toBeInTheDocument()
    const post = calls.find((c) => c.method === 'POST')!
    expect(post.body).toEqual({ username: 'alice', password: 'correct horse battery' })
    expect(post.headers['Content-Type']).toBe('application/json')
  })

  it('associates empty-field errors with their fields', async () => {
    const user = userEvent.setup()
    installApi(signedOut)
    renderApp('/sign-in')
    await user.click(await screen.findByRole('button', { name: 'Sign in' }))
    const u = screen.getByLabelText('Username')
    expect(u).toBeInvalid()
    expect(u).toHaveAccessibleDescription('Enter your username.')
    expect(screen.getByLabelText('Password')).toHaveAccessibleDescription('Enter your password.')
  })

  it('shows invalid credentials as one alert that takes focus, never saying which part was wrong', async () => {
    const user = userEvent.setup()
    installApi({ ...signedOut, 'POST /api/v1/auth/sign-in': fail(401, 'invalid_credentials', 'Invalid username or password.') })
    renderApp('/sign-in')
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'wrong{Enter}')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Invalid username or password.')
    expect(alert).toHaveFocus()
  })

  it('says when to retry after a rate limit', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedOut,
      'POST /api/v1/auth/sign-in': { status: 429, json: { code: 'rate_limited', message: 'slow down', request_id: 'r' }, headers: { 'Retry-After': '90' } },
    })
    renderApp('/sign-in')
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'x{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent('Try again in 90 seconds.')
  })

  it('takes the second step with a code, and field-associates a wrong code', async () => {
    const user = userEvent.setup()
    let attempts = 0
    const calls = installApi({
      ...signedOut,
      'POST /api/v1/auth/sign-in': ok({ second_factor_required: true, challenge: 'chal', expires_at: '2026-11-21T10:05:00Z', methods: ['totp', 'recovery_code'] }, 202),
      'POST /api/v1/auth/sign-in/second-factor': () =>
        ++attempts === 1 ? fail(401, 'invalid_second_factor', 'That code is not valid.') : ok(session()),
    })
    renderApp('/sign-in')
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'pw{Enter}')
    const code = await screen.findByLabelText('Authenticator code')
    expect(code).toHaveFocus()
    await user.type(code, '000000{Enter}')
    await waitFor(() => expect(screen.getByLabelText('Authenticator code')).toBeInvalid())
    expect(screen.getByLabelText('Authenticator code')).toHaveAccessibleDescription(/That code is not valid\./)
    expect(calls.at(-1)!.body).toEqual({ challenge: 'chal', code: '000000' })
    // The password is not kept: starting over shows an empty password field.
  })

  it('switches to a recovery code, and starts over when the challenge has expired', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedOut,
      'POST /api/v1/auth/sign-in': ok({ second_factor_required: true, challenge: 'chal', expires_at: '2026-11-21T10:05:00Z', methods: ['totp'] }, 202),
      'POST /api/v1/auth/sign-in/second-factor': fail(401, 'unauthenticated', 'sign in again'),
    })
    renderApp('/sign-in')
    await user.type(await screen.findByLabelText('Username'), 'alice')
    await user.type(screen.getByLabelText('Password'), 'pw{Enter}')
    await user.click(await screen.findByRole('button', { name: 'Use a recovery code' }))
    await user.type(screen.getByLabelText('Recovery code'), 'AAAA-BBBB-CCCC-DDDD{Enter}')
    expect(await screen.findByRole('alert')).toHaveTextContent('expired')
    expect(calls.filter((c) => c.method === 'POST').at(-1)!.body).toEqual({ challenge: 'chal', recovery_code: 'AAAA-BBBB-CCCC-DDDD' })
    expect(screen.getByLabelText('Password')).toHaveValue('')
  })
})
