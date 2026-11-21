import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, ok, renderApp, session, signedIn } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

describe('required-2FA gate', () => {
  const confined = session({ two_factor: { enabled: false, required: true, enrollment_required: true } })

  it('sends a confined session to enrolment and offers nothing else', async () => {
    installApi({
      ...signedIn(confined),
      'GET /api/v1/auth/two-factor': ok({ enabled: false, method: null, enabled_at: null, recovery_codes_remaining: 0, required: true, pending_enrollment_until: null }),
    })
    renderApp('/account/tokens')
    expect(await screen.findByRole('heading', { name: 'Account security' })).toBeInTheDocument()
    expect(screen.getByRole('note')).toHaveTextContent('requires two-factor');
    const nav = screen.getByRole('navigation', { name: 'Main' })
    expect(within(nav).queryByRole('link', { name: 'API tokens' })).not.toBeInTheDocument()
    expect(within(nav).queryByRole('link', { name: 'Sessions' })).not.toBeInTheDocument()
    expect(within(nav).getByRole('button', { name: 'Sign out' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'Change password' })).not.toBeInTheDocument()
  })

  it('moves to enrolment when a request answers two_factor_enrollment_required', async () => {
    let confinedNow = false
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/session': () => ok(confinedNow ? confined : session()),
      'GET /api/v1/auth/sessions': () => {
        confinedNow = true
        return fail(403, 'two_factor_enrollment_required', 'Set up two-factor authentication first.')
      },
      'GET /api/v1/auth/two-factor': ok({ enabled: false, method: null, enabled_at: null, recovery_codes_remaining: 0, required: true, pending_enrollment_until: null }),
    })
    renderApp('/account/sessions')
    expect(await screen.findByRole('heading', { name: 'Account security' })).toBeInTheDocument()
  })
})

describe('re-authentication prompt', () => {
  it('asks for the password, then runs the action again', async () => {
    const user = userEvent.setup()
    let needs = true
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok({ enabled: true, method: 'totp', enabled_at: '2026-11-01T10:00:00Z', recovery_codes_remaining: 4, required: false, pending_enrollment_until: null }),
      'POST /api/v1/auth/reauthenticate': () => {
        needs = false
        return ok({ authenticated_at: 'x', recent_auth_until: 'y' })
      },
      'POST /api/v1/auth/two-factor/recovery-codes': () =>
        needs ? fail(403, 'reauthentication_required', 'Confirm your password.') : ok({ recovery_codes: ['AAAA-BBBB-CCCC-DDDD'] }),
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Regenerate recovery codes' }))
    await user.click(await screen.findByRole('button', { name: 'Replace codes' }))
    const dlg = await screen.findByRole('dialog', { name: 'Confirm your password' })
    expect(dlg).toHaveAccessibleDescription(/has not been made yet/)
    await user.type(within(dlg).getByLabelText('Password'), 'pw{Enter}')
    // The original request ran again and its result is shown.
    expect(await screen.findByRole('dialog', { name: 'Your recovery codes' })).toBeInTheDocument()
    expect(screen.getByText('AAAA-BBBB-CCCC-DDDD')).toBeInTheDocument()
    const posts = calls.filter((c) => c.path.endsWith('recovery-codes'))
    expect(posts).toHaveLength(2)
    expect(posts[0]!.headers['X-CSRF-Token']).toBe('csrf-abc')
  })

  it('keeps the action undone when the prompt is cancelled', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok({ enabled: true, method: 'totp', enabled_at: null, recovery_codes_remaining: 4, required: false, pending_enrollment_until: null }),
      'POST /api/v1/auth/two-factor/recovery-codes': fail(403, 'reauthentication_required', 'Confirm your password.'),
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Regenerate recovery codes' }))
    await user.click(await screen.findByRole('button', { name: 'Replace codes' }))
    await user.click(await screen.findByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog', { name: 'Your recovery codes' })).not.toBeInTheDocument()
    expect(await screen.findByRole('alert')).toHaveTextContent('Confirm your password.')
  })
})
