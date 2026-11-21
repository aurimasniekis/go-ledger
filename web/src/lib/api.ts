// Minimal API client. Server state is read through TanStack Query hooks built on
// apiFetch; nothing here caches or copies it.

/** One invalid field from the error envelope's errors[] (ADR-0015). */
export interface FieldDetail {
  field: string
  message: string
}

/** The JSON error envelope the server sends for every non-2xx API response (ADR-0006). */
export interface ErrorBody {
  code: string
  message: string
  field?: string
  errors?: FieldDetail[]
  request_id: string
  reconciliation?: { current_version: number; current?: unknown }
}

/** The request never reached the server (offline, refused, DNS). Any other thrown error is a defect, not a connection problem. */
export class NetworkError extends Error {
  constructor(cause?: unknown) {
    super('Cannot reach the server.', { cause })
    this.name = 'NetworkError'
  }
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly field?: string
  /** Every invalid field; the first entry repeats field and message. Empty when the error is not about fields. */
  readonly errors: FieldDetail[]
  readonly requestId?: string
  /** Seconds from Retry-After (rate_limited), when the server sent one. */
  readonly retryAfter?: number
  /** On a version conflict (409/412): the server's current version, and its current state when sent. */
  readonly reconciliation?: ErrorBody['reconciliation']

  constructor(status: number, body: Partial<ErrorBody>, retryAfter?: number) {
    super(body.message ?? `request failed with status ${status}`)
    this.name = 'ApiError'
    this.status = status
    this.code = body.code ?? 'unknown'
    this.field = body.field
    this.errors = body.errors?.length ? body.errors : body.field ? [{ field: body.field, message: this.message }] : []
    this.requestId = body.request_id
    this.retryAfter = retryAfter
    this.reconciliation = body.reconciliation
  }
}

// The CSRF token of the signed-in session (ADR-0026 §6). It comes from sign-in and
// GET /auth/session, lives only in memory and is sent with every state-changing request.
let csrfToken: string | null = null
export function setCsrfToken(token: string | null) {
  csrfToken = token
}

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

// When the server asks for a recent password check (403 reauthentication_required) the
// handler prompts the user; true means the check passed and the request is retried once.
type ReauthHandler = () => Promise<boolean>
let reauthHandler: ReauthHandler | null = null
export function setReauthHandler(h: ReauthHandler | null) {
  reauthHandler = h
}

export interface ApiInit extends Omit<RequestInit, 'body'> {
  /** A value sent as a JSON body (Content-Type application/json, or the merge-patch type for PATCH). */
  json?: unknown
  /** Never prompt for re-authentication (used by the re-authentication request itself). */
  noReauth?: boolean
}

/** Fetches `path` (which must start with /api/) and returns the parsed JSON body. */
export async function apiFetch<T>(path: string, init: ApiInit = {}): Promise<T> {
  if (!path.startsWith('/api/')) throw new Error(`API paths start with /api/: ${path}`)
  const { json, noReauth, ...rest } = init
  const method = (rest.method ?? 'GET').toUpperCase()
  const headers: Record<string, string> = { Accept: 'application/json', ...(rest.headers as Record<string, string>) }
  if (!SAFE_METHODS.has(method) && csrfToken) headers['X-CSRF-Token'] = csrfToken
  let body: string | undefined
  if (json !== undefined) {
    body = JSON.stringify(json)
    headers['Content-Type'] = method === 'PATCH' ? 'application/merge-patch+json' : 'application/json'
  }
  let res: Response
  try {
    res = await fetch(path, { ...rest, method, headers, body })
  } catch (e) {
    // fetch rejects with a TypeError when the request could not be made; an abort is not a connection problem.
    if (e instanceof TypeError) throw new NetworkError(e)
    throw e
  }
  if (!res.ok) {
    let errBody: Partial<ErrorBody> = {}
    try {
      errBody = (await res.json()) as Partial<ErrorBody>
    } catch {
      // A proxy or crash page: fall back to the status text.
    }
    const ra = Number(res.headers.get('Retry-After'))
    const err = new ApiError(res.status, errBody, Number.isFinite(ra) && ra > 0 ? ra : undefined)
    if (err.code === 'reauthentication_required' && reauthHandler && !noReauth && (await reauthHandler())) {
      return apiFetch<T>(path, { ...init, noReauth: true })
    }
    throw err
  }
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

/** Field-associated messages from an error, keyed by request field; and what is left for a form-level message. */
export function splitErrors(err: unknown, known: readonly string[]): { fields: Record<string, string>; form?: string } {
  if (err === null || err === undefined) return { fields: {} }
  if (!(err instanceof ApiError)) return { fields: {}, form: err instanceof Error ? err.message : 'Something went wrong.' }
  const fields: Record<string, string> = {}
  const other: string[] = []
  for (const e of err.errors) {
    // Nested members are named with dots (schedule.day): match on the top-level name too.
    const key = known.includes(e.field) ? e.field : known.find((k) => e.field.startsWith(k + '.'))
    if (key) fields[key] ??= e.message
    else other.push(e.message)
  }
  if (err.errors.length === 0) return { fields, form: err.message }
  return { fields, form: other.length ? other.join(' ') : undefined }
}
