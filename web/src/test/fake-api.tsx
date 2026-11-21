import { render } from '@testing-library/react'
import type { QueryClient } from '@tanstack/react-query'
import { createMemoryHistory } from '@tanstack/react-router'
import { vi } from 'vitest'
import { App } from '@/App'
import type { ApiToken, CurrentSession, GrantVocabulary, Principal, Session, User } from '@/lib/types'

export interface Req {
  method: string
  path: string
  query: URLSearchParams
  body: unknown
  headers: Record<string, string>
}
export type Reply = { status?: number; json?: unknown; headers?: Record<string, string> }
export type Handler = Reply | ((req: Req) => Reply | Promise<Reply>)

export const ok = (json: unknown, status = 200): Reply => ({ status, json })
export const none = (): Reply => ({ status: 204 })
export const fail = (status: number, code: string, message: string, extra: Record<string, unknown> = {}): Reply => ({
  status,
  json: { code, message, request_id: 'req_test', ...extra },
})

/** Stubs fetch with a route table keyed "METHOD /api/v1/path" (no query). Returns the recorded calls. */
export function installApi(routes: Record<string, Handler>) {
  const calls: Req[] = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: string, init: RequestInit = {}) => {
      const url = new URL(input, 'http://localhost')
      const req: Req = {
        method: (init.method ?? 'GET').toUpperCase(),
        path: url.pathname,
        query: url.searchParams,
        body: init.body ? JSON.parse(init.body as string) : undefined,
        headers: (init.headers ?? {}) as Record<string, string>,
      }
      calls.push(req)
      const h = routes[`${req.method} ${req.path}`]
      const reply = await (h === undefined ? fail(404, 'not_found', `no fake for ${req.method} ${req.path}`) : typeof h === 'function' ? h(req) : h)
      const status = reply.status ?? 200
      return new Response(status === 204 ? null : JSON.stringify(reply.json ?? {}), { status, headers: reply.headers })
    }),
  )
  return calls
}

export function renderApp(path: string, client?: QueryClient) {
  return render(<App history={createMemoryHistory({ initialEntries: [path] })} client={client} />)
}

const now = '2026-11-21T10:00:00Z'
export const session = (over: Partial<CurrentSession> = {}): CurrentSession => ({
  user_id: '01JD3Y4C8V9M2Q7W5R6T0XZBNA',
  session: sess({ current: true }),
  csrf_token: 'csrf-abc',
  recent_auth_until: now,
  two_factor: { enabled: false, required: false, enrollment_required: false },
  ...over,
})
export const sess = (over: Partial<Session> = {}): Session => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZB01',
  current: false,
  created_at: now,
  last_seen_at: now,
  authenticated_at: now,
  idle_expires_at: now,
  expires_at: now,
  user_agent: 'TestBrowser/1.0',
  address: '192.0.2.1',
  ...over,
})
export const ALL_GRANTS = ['audit:read', 'banking:read', 'commitments:read', 'commitments:write', 'connections:read', 'connections:write', 'tokens:read', 'tokens:write', 'users:read', 'users:write']
export const admin = (grants = ALL_GRANTS): Principal => ({
  kind: 'user',
  user_id: '01JD3Y4C8V9M2Q7W5R6T0XZBNA',
  token_id: null,
  role: 'administrator',
  grants,
})
export const vocab: GrantVocabulary = {
  resources: [
    { name: 'commitments', description: 'Recurring commitments', actions: ['read', 'write'], admin_only: false },
    { name: 'connections', description: 'Bank connections', actions: ['read', 'write'], admin_only: true },
    { name: 'users', description: 'Users and roles', actions: ['read', 'write'], admin_only: true },
    { name: 'tokens', description: 'API tokens of every user', actions: ['read', 'write'], admin_only: true },
    { name: 'audit', description: 'The audit log', actions: ['read'], admin_only: true },
  ],
  roles: [
    { name: 'administrator', grants: ALL_GRANTS },
    { name: 'read-only', grants: ['commitments:read'] },
  ],
}
export const token = (over: Partial<ApiToken> = {}): ApiToken => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZT01',
  name: 'backup script',
  owner_id: '01JD3Y4C8V9M2Q7W5R6T0XZBNA',
  root: false,
  grants: ['commitments:read'],
  hint: 'ldgr_AbCd…wXyZ',
  status: 'active',
  created_at: now,
  expires_at: null,
  last_used_at: null,
  revoked_at: null,
  ...over,
})
export const user = (over: Partial<User> = {}): User => ({
  id: '01JD3Y4C8V9M2Q7W5R6T0XZBNA',
  username: 'alice',
  display_name: null,
  role: 'administrator',
  disabled: false,
  disabled_at: null,
  version: 1,
  created_at: now,
  updated_at: now,
  ...over,
})
/** The routes every signed-in page needs. */
export const signedIn = (s = session(), grants = ALL_GRANTS): Record<string, Handler> => ({
  'GET /api/v1/auth/session': ok(s),
  'GET /api/v1/me': ok(admin(grants)),
  'GET /api/v1/grants': ok(vocab),
})
