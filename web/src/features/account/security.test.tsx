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

  it('turning two-factor off asks for a current code and is only reported after the server accepts it', async () => {
    const user = userEvent.setup()
    let on = true
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': () => ok(on ? status({ enabled: true, method: 'totp', recovery_codes_remaining: 3 }) : status()),
      'DELETE /api/v1/auth/two-factor': (r) => {
        if ((r.body as { current_code?: string } | undefined)?.current_code !== '654321')
          return fail(400, 'invalid_second_factor', 'The current authentication code or recovery code is not valid.', { field: 'current_code' })
        on = false
        return none()
      },
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Turn off two-factor' }))
    const dlg = await screen.findByRole('alertdialog')
    expect(dlg).toHaveAccessibleDescription(/recovery codes stop working/)
    // Nothing typed: refused locally, nothing sent.
    await user.click(within(dlg).getByRole('button', { name: 'Turn off' }))
    const field = within(dlg).getByLabelText('Current code from your authenticator')
    expect(field).toHaveAccessibleDescription(/Enter the current 6-digit code/)
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    // A wrong code: the error is on the field and the dialog stays open.
    await user.type(field, '000000{Enter}')
    expect(await within(dlg).findByText('The current authentication code or recovery code is not valid.')).toBeInTheDocument()
    expect(field).toBeInvalid()
    expect(screen.getByText(/authenticator app since/)).toBeInTheDocument()
    await user.clear(field)
    await user.type(field, '654321{Enter}')
    expect(await screen.findByText(/Signing in needs only your password/)).toBeInTheDocument()
    expect(calls.filter((c) => c.method === 'DELETE').map((c) => c.body)).toEqual([{ current_code: '000000' }, { current_code: '654321' }])
  })

  it('replacing recovery codes can be confirmed with a recovery code instead', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok(status({ enabled: true, method: 'totp', recovery_codes_remaining: 3 })),
      'POST /api/v1/auth/two-factor/recovery-codes': ok({ recovery_codes: CODES }),
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Regenerate recovery codes' }))
    const dlg = await screen.findByRole('alertdialog')
    await user.type(within(dlg).getByLabelText('Current code from your authenticator'), '12')
    await user.click(within(dlg).getByRole('button', { name: 'Use a recovery code instead' }))
    const rc = within(dlg).getByLabelText('One of your recovery codes')
    // Switching clears what was typed.
    expect(rc).toHaveValue('')
    await user.type(rc, 'WXYZ-WXYZ-WXYZ-WXYZ')
    await user.click(within(dlg).getByRole('button', { name: 'Replace codes' }))
    const shown = await screen.findByRole('dialog', { name: 'Your recovery codes' })
    expect(within(shown).getByText('AAAA-BBBB-CCCC-DDDD')).toBeInTheDocument()
    expect(calls.find((c) => c.path.endsWith('/recovery-codes'))?.body).toEqual({ current_recovery_code: 'WXYZ-WXYZ-WXYZ-WXYZ' })
  })

  it('replacing the authenticator asks for the new code and a current code', async () => {
    const user = userEvent.setup()
    const calls = installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok(status({ enabled: true, method: 'totp', recovery_codes_remaining: 3 })),
      'POST /api/v1/auth/two-factor/totp': ok({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/x', expires_at: '2026-11-21T10:10:00Z' }, 201),
      'POST /api/v1/auth/two-factor/totp/confirm': (r) => {
        const b = r.body as { code: string; current_code?: string }
        if (b.current_code !== '222222')
          return fail(400, 'invalid_second_factor', 'The current authentication code or recovery code is not valid.', { field: 'current_code' })
        return ok({ recovery_codes: CODES, sessions_ended: 0 })
      },
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Replace authenticator' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    const newCode = await screen.findByLabelText('Code from your new authenticator')
    const current = screen.getByLabelText('Current code from your authenticator')
    await user.type(newCode, '111111{Enter}')
    expect(current).toHaveAccessibleDescription(/Enter the current 6-digit code/)
    expect(calls.some((c) => c.path.endsWith('/confirm'))).toBe(false)
    await user.type(current, '999999{Enter}')
    expect(await screen.findByText('The current authentication code or recovery code is not valid.')).toBeInTheDocument()
    expect(current).toBeInvalid()
    expect(newCode).not.toBeInvalid()
    await user.clear(current)
    await user.type(current, '222222{Enter}')
    expect(await screen.findByRole('dialog', { name: 'Your recovery codes' })).toBeInTheDocument()
    expect(calls.filter((c) => c.path.endsWith('/confirm')).at(-1)?.body).toEqual({ code: '111111', current_code: '222222' })
  })

  it('a first enrolment does not ask for a current code', async () => {
    const user = userEvent.setup()
    installApi({
      ...signedIn(),
      'GET /api/v1/auth/two-factor': ok(status()),
      'POST /api/v1/auth/two-factor/totp': ok({ secret: 'JBSWY3DPEHPK3PXP', otpauth_uri: 'otpauth://totp/x', expires_at: '2026-11-21T10:10:00Z' }, 201),
    })
    renderApp('/account/security')
    await user.click(await screen.findByRole('button', { name: 'Set up an authenticator' }))
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await screen.findByLabelText('Code from your authenticator')
    expect(screen.queryByLabelText('Current code from your authenticator')).not.toBeInTheDocument()
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
