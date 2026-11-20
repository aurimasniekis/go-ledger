// Minimal API client. Server state is read through TanStack Query hooks built on
// apiFetch; nothing here caches or copies it.

/** The JSON error envelope the server sends for every non-2xx API response (ADR-0006). */
export interface ErrorBody {
  code: string
  message: string
  field?: string
  request_id: string
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly field?: string
  readonly requestId?: string

  constructor(status: number, body: Partial<ErrorBody>) {
    super(body.message ?? `request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code ?? 'unknown'
    this.field = body.field
    this.requestId = body.request_id
  }
}

/** Fetches `path` (which must start with /api/) and returns the parsed JSON body. */
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  if (!path.startsWith('/api/')) throw new Error(`API paths start with /api/: ${path}`)
  const res = await fetch(path, {
    ...init,
    headers: { Accept: 'application/json', ...init?.headers },
  })
  if (!res.ok) {
    let body: Partial<ErrorBody> = {}
    try {
      body = (await res.json()) as Partial<ErrorBody>
    } catch {
      // A proxy or crash page: fall back to the status text.
    }
    throw new ApiError(res.status, body)
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}
