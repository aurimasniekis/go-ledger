import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { fail, installApi, none, ok, renderApp, session, signedIn } from '@/test/fake-api'
import { setCsrfToken } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
  setCsrfToken(null)
})

const status = (over = {}) => ({ enabled: false, method: null, enabled_at: null, recovery_codes_remaining: 0, required: false, pending_enrollment_until: null, ...over })
const CODES = ['AAAA-BBBB-CCCC-DDDD', 'EEEE-FFFF-GGGG-HHHH']

describe('two-factor enrolment', () => {
  it('shows the key and link, confirms with a code, and shows recovery codes once', async () => {
    const user = userEvent.setup()
    let on = false
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': () => ok(on ? status({ enabled: true, method: 'totp', recovery_codes_remaining: 2 }) : status()),
      'POST /api/v1/auth/two-factor/totp': ok({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/Ledger:alice?secret=JBSWY3DPEHPK3PXP', expires_at: '2026-11-21T10:10:00Z' }, 201),
      'POST /api/v1/auth/two-factor/totp/confirm': (r) => {
        if ((r.body as { code: string }).code !== '123456') return fail(400, 'invalid_second_factor', 'That code is not valid.')
        on = true
        return ok({ recovery_codes: CODES, sessions_ended: 1 })
      },
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Set up an authenticator' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    expect(await screen.findByLabelText('Setup key')).toHaveTextContent('JBSWY3DPEHPK3PXP')
    expect(screen.getByText(/does not draw a QR code/)).toBeInTheDocument()
    const code = screen.getByLabelText('Code from your authenticator')
    expect(code).toHaveFocus()
    await user.type(code, '000000{Enter}')
    expect(await screen.findByText('That code is not valid.')).toBeInTheDocument()
    expect(code).toBeInvalid()
    await user.clear(code)
    await user.type(code, '123456{Enter}')
    const dlg = await screen.findByRole('dialog', { name: 'Your recovery codes' })
    for (const c of CODES) expect(within(dlg).getByText(c)).toBeInTheDocument()
    expect(calls.some((c) => c.path.endsWith('/confirm') && c.headers['X-CSRF-Token'] === 'csrf-abc')).toBe(true)
    await user.click(within(dlg).getByRole('button', { name: 'Done' }))
    expect(document.body.textContent).not.toContain(CODES[0])
    // The control that started it is gone; focus lands on the page heading, not the body.
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Account security' })).toHaveFocus())
    expect(await screen.findByText(/authenticator app since/)).toBeInTheDocument()
    // The setup key is gone as well.
    expect(screen.queryByLabelText('Setup key')).not.toBeInTheDocument()
  })

  it('turning two-factor off is confirmed and only reported after the server accepts it', async () => {
    const user = userEvent.setup()
    let on = true
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': () => ok(on ? status({ enabled: true, method: 'totp', recovery_codes_remaining: 3 }) : status()),
      'DELETE /api/v1/auth/two-factor': () => {
        on = false
        return none()
      },
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Turn off two-factor' }))
    expect(await screen.findByRole('alertdialog')).toHaveAccessibleDescription(/recovery codes stop working/)
    await user.click(screen.getByRole('button', { name: 'Turn off' }))
    expect(await screen.findByText(/Signing in needs only your password/)).toBeInTheDocument()
  })

  it('cannot turn off two-factor when the server requires it, and says why', async () => {
    installApi({ ...signedIn(), 'GET /api/v1/auth/two-factor': ok(status({ enabled: true, method: 'totp', required: true, recovery_codes_remaining: 3 })) })
    renderApp('/account/security')
    await screen.findByRole('button', { name: 'Replace authenticator' })
    expect(screen.queryByRole('button', { name: 'Turn off two-factor' })).not.toBeInTheDocument()
    expect(screen.getByText(/cannot be turned off while this Ledger requires it/)).toBeInTheDocument()
  })

  it('lifts the confinement after confirming (the session is refetched)', async () => {
    const user = userEvent.setup()
    let confined = true
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/session': () => ok(session({ two_factor: { enabled: !confined, required: true, enrollment_required: confined } })),
      'GET /api/v1/auth/two-factor': ok(status({ required: true })),
      'POST /api/v1/auth/two-factor/totp': ok({ secret: 'ABCDEFGH', otpauth_uri: 'otpauth://totp/x', expires_at: '2026-11-21T10:10:00Z' }, 201),
      'POST /api/v1/auth/two-factor/totp/confirm': () => {
        confined = false
        return ok({ recovery_codes: CODES })
      },
    })
    renderApp('/account/tokens')
    await user.click(await screen.findByRole('button', { name: 'Set up an authenticator' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await user.type(await screen.findByLabelText('Code from your authenticator'), '123456{Enter}')
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Done' }))
    expect(await screen.findByRole('link', { name: 'API tokens' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Change password' })).toBeInTheDocument()
  })
})

describe('change password', () => {
  it('checks the repeat client-side and reports the server result', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok(status()),
      'PUT /api/v1/auth/password': (r) =>
        (r.body as { password: string }).password.length < 12
          ? fail(400, 'validation_failed', 'password too short', { field: 'password', errors: [{ field: 'password', message: 'must be at least 12 bytes' }] })
          : ok({ sessions_ended: 2 }),
    })
    renderApp('/account/security')
    await user.type(await screen.findByLabelText('New password'), 'short')
    await user.type(screen.getByLabelText('Repeat the new password'), 'other{Enter}')
    expect(screen.getByLabelText('Repeat the new password')).toHaveAccessibleDescription('The two passwords do not match.')
    await user.clear(screen.getByLabelText('Repeat the new password'))
    await user.type(screen.getByLabelText('Repeat the new password'), 'short{Enter}')
    expect(await screen.findByText('must be at least 12 bytes')).toBeInTheDocument()
    expect(screen.getByLabelText('New password')).toBeInvalid()
    for (const l of ['New password', 'Repeat the new password']) {
      await user.clear(screen.getByLabelText(l))
      await user.type(screen.getByLabelText(l), 'a much longer password')
    }
    await user.keyboard('{Enter}')
    expect(await screen.findByText(/2 other session\(s\) were signed out/)).toBeInTheDocument()
  })
})
