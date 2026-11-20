import { useState } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { makeQueryClient } from '@/lib/query'
import { makeRouter } from '@/router'

export function App({ history }: { history?: Parameters<typeof makeRouter>[1] }) {
  const [queryClient] = useState(makeQueryClient)
  const [router] = useState(() => makeRouter({ queryClient }, history))
  return (
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}
