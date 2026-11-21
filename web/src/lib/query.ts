import { MutationCache, QueryCache, QueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'

// A response that says the session is gone, or confined to enrolment, means the cached
// session is out of date: refetch it so the gate in RequireSession reacts. The session
// query itself is excluded, or a 401 there would refetch forever.
function sessionChanged(client: () => QueryClient) {
  return (err: unknown, key: readonly unknown[] | undefined) => {
    if (!(err instanceof ApiError) || key?.[0] === 'session') return
    if (err.code === 'two_factor_enrollment_required' || err.code === 'unauthenticated') {
      void client().invalidateQueries({ queryKey: ['session'] })
    }
  }
}

export function makeQueryClient() {
  const react = sessionChanged(() => client)
  const client: QueryClient = new QueryClient({
    queryCache: new QueryCache({ onError: (err, q) => react(err, q.queryKey) }),
    mutationCache: new MutationCache({ onError: (err, _v, _c, m) => react(err, m.options.mutationKey) }),
    defaultOptions: {
      queries: {
        // A 4xx will not change on retry; transient failures get two more tries.
        retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
        refetchOnWindowFocus: true,
      },
    },
  })
  return client
}
