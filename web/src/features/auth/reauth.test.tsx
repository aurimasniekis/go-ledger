import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch } from '@/lib/api'
import { ReauthProvider } from '@/features/auth/reauth'
import { fail, installApi } from '@/test/fake-api'

afterEach(() => vi.unstubAllGlobals())

describe('ReauthProvider', () => {
  it('settles waiting requests when it unmounts with the prompt open', async () => {
    installApi({ 'POST /api/v1/thing': fail(403, 'reauthentication_required', 'confirm your password') })
    const view = render(
      <QueryClientProvider client={new QueryClient()}>
        <ReauthProvider>
          <p>page</p>
        </ReauthProvider>
      </QueryClientProvider>,
    )
    const pending = apiFetch('/api/v1/thing', { method: 'POST' }).then(
      () => 'resolved',
      (e: unknown) => e,
    )
    await screen.findByRole('dialog', { name: 'Confirm your password' })
    view.unmount()
    // Cancelling semantics: the original request fails with the server's error instead of hanging.
    const out = await pending
    expect(out).toBeInstanceOf(ApiError)
    expect((out as ApiError).code).toBe('reauthentication_required')
  })
})
