import { useQuery, useQueryClient } from '@tanstack/react-query'
import { apiFetch, setCsrfToken } from '@/lib/api'
import type { CurrentSession, Principal } from '@/lib/types'

const V1 = '/api/v1'
export const api = (path: string) => `${V1}${path}`

/** The signed-in session. A 401 is the answer "nobody is signed in" and is not retried. */
export function useSession() {
  return useQuery({
    queryKey: ['session'],
    queryFn: async () => {
      const s = await apiFetch<CurrentSession>(api('/auth/session'))
      setCsrfToken(s.csrf_token)
      return s
    },
    staleTime: 30_000,
  })
}

/** Stores a session from sign-in: the CSRF token for writes, and the session for the gate. */
export function useStartSession() {
  const qc = useQueryClient()
  return (s: CurrentSession) => {
    setCsrfToken(s.csrf_token)
    qc.setQueryData(['session'], s)
    // Everything cached belongs to the previous user, if any.
    void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'session' })
  }
}

/** The principal and its effective grants (what the UI may offer). */
export function useMe() {
  const session = useSession()
  return useQuery({ queryKey: ['me'], queryFn: () => apiFetch<Principal>(api('/me')), enabled: !!session.data })
}

export function holds(grants: readonly string[] | undefined, grant: string): boolean {
  if (!grants) return false
  if (grants.includes(grant)) return true
  // A write grant also allows reading the same resource (ADR-0021).
  const [res, action] = grant.split(':')
  return action === 'read' && grants.includes(`${res}:write`)
}
