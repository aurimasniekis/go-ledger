import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, apiFetch } from '@/lib/api'

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

  it('rejects paths outside /api/', async () => {
    await expect(apiFetch('/other')).rejects.toThrow('/api/')
  })
})
