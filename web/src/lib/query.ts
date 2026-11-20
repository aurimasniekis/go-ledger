import { QueryClient } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'

export function makeQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        // A 4xx will not change on retry; transient failures get two more tries.
        retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
        refetchOnWindowFocus: true,
      },
    },
  })
}
