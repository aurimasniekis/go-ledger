import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch, setCsrfToken, setReauthHandler, splitErrors } from '@/lib/api'

afterEach(() => vi.unstubAllGlobals())

describe('apiFetch', () => {
  it('parses the error envelope into an ApiError', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(JSON.stringify({ code: 'not_found', message: 'no such resource', request_id: 'r1' }), {
          status: 404,
        }),
      ),
    )
    const err = await apiFetch('/api/x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 404, code: 'not_found', requestId: 'r1', message: 'no such resource' })
  })

  it('survives a non-JSON failure body', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>bad gateway</html>', { status: 502 })))
    await expect(apiFetch('/api/x')).rejects.toMatchObject({ status: 502, code: 'unknown' })
  })

  it('keeps errors[] and Retry-After from the envelope', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({ code: 'validation_failed', message: 'invalid', field: 'a', errors: [{ field: 'a', message: 'bad a' }, { field: 'schedule.day', message: 'bad day' }], request_id: 'r' }),
          { status: 400, headers: { 'Retry-After': '7' } },
        ),
      ),
    )
    const err = (await apiFetch('/api/x').catch((e: unknown) => e)) as ApiError
    expect(err.errors).toHaveLength(2)
    expect(err.retryAfter).toBe(7)
    expect(splitErrors(err, ['a', 'schedule'])).toEqual({ fields: { a: 'bad a', schedule: 'bad day' }, form: undefined })
    expect(splitErrors(err, ['a']).form).toBe('bad day')
    expect(splitErrors(new ApiError(401, { code: 'invalid_credentials', message: 'no' }), [])).toEqual({ fields: {}, form: 'no' })
  })

  it('sends the CSRF token on writes only, and JSON bodies with their type', async () => {
    const f = vi.fn().mockImplementation(() => Promise.resolve(new Response('{}', { status: 200 })))
    vi.stubGlobal('fetch', f)
    setCsrfToken('tok')
    await apiFetch('/api/x')
    await apiFetch('/api/x', { method: 'POST', json: { a: 1 } })
    await apiFetch('/api/x', { method: 'PATCH', json: { a: 1 } })
    setCsrfToken(null)
    const h = (i: number) => (f.mock.calls[i] as [string, RequestInit])[1].headers as Record<string, string>
    expect(h(0)['X-CSRF-Token']).toBeUndefined()
    expect(h(1)['X-CSRF-Token']).toBe('tok')
    expect(h(1)['Content-Type']).toBe('application/json')
    expect(h(2)['Content-Type']).toBe('application/merge-patch+json')
  })

  it('retries once after the re-authentication handler confirms, and not when it declines', async () => {
    const reauth = new Response(JSON.stringify({ code: 'reauthentication_required', message: 'confirm', request_id: 'r' }), { status: 403 })
    const f = vi.fn().mockResolvedValueOnce(reauth).mockResolvedValueOnce(new Response('{"ok":true}', { status: 200 }))
    vi.stubGlobal('fetch', f)
    setReauthHandler(async () => true)
    await expect(apiFetch('/api/x', { method: 'POST' })).resolves.toEqual({ ok: true })
    expect(f).toHaveBeenCalledTimes(2)
    f.mockReset().mockResolvedValue(new Response(JSON.stringify({ code: 'reauthentication_required', message: 'confirm', request_id: 'r' }), { status: 403 }))
    setReauthHandler(async () => false)
    await expect(apiFetch('/api/x', { method: 'POST' })).rejects.toMatchObject({ code: 'reauthentication_required' })
    expect(f).toHaveBeenCalledTimes(1)
    setReauthHandler(null)
  })

  it('rejects paths outside /api/', async () => {
    await expect(apiFetch('/other')).rejects.toThrow('/api/')
  })
})
