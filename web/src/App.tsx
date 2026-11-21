import { useState } from 'react'
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { makeQueryClient } from '@/lib/query'
import { makeRouter } from '@/router'

export function App({ history, client }: { history?: Parameters<typeof makeRouter>[1]; client?: QueryClient }) {
  const [queryClient] = useState(() => client ?? makeQueryClient())
  const [router] = useState(() => makeRouter({ queryClient }, history))
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
