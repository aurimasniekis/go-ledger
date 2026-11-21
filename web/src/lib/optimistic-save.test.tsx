import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { AnnouncerProvider } from '@/components/announcer'
import { useOptimisticSave } from '@/lib/optimistic-save'

type List = string[]
const KEY = ['list']

function Host({ server, optimistic }: { server: { items: List; fail: boolean; gate?: Promise<void> }; optimistic: boolean }) {
  const list = useQuery({ queryKey: KEY, queryFn: async () => [...server.items] })
  const s = useOptimisticSave<string, string, List>({
    queryKey: KEY,
    mutationFn: async (v) => {
      await server.gate
      if (server.fail) throw new Error('server said no')
      server.items.push(v)
      return v
    },
    apply: optimistic ? (c, v) => [...(c ?? []), v] : undefined,
    messages: { saved: (v) => `Added ${v}.` },
  })
  return (
    <div>
      <ul aria-label="items">{(list.data ?? []).map((i) => <li key={i}>{i}</li>)}</ul>
      <button onClick={() => s.save('b')}>add</button>
      <button onClick={s.retry}>retry</button>
      <button onClick={s.discard}>discard</button>
      <p data-testid="phase">{s.phase}</p>
      <p data-testid="vars">{s.vars ?? '-'}</p>
      <p data-testid="err">{s.error?.message ?? '-'}</p>
    </div>
  )
}

function setup(server: { items: List; fail: boolean; gate?: Promise<void> }, optimistic: boolean) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={qc}>
      <AnnouncerProvider><Host server={server} optimistic={optimistic} /></AnnouncerProvider>
    </QueryClientProvider>,
  )
  return qc
}
const items = () => Array.from(screen.getByRole('list', { name: 'items' }).querySelectorAll('li')).map((l) => l.textContent)

describe('useOptimisticSave', () => {
  it('shows the change at once, stays pending until the server confirms, then reconciles', async () => {
    let release!: () => void
    const server = { items: ['a'], fail: false, gate: new Promise<void>((r) => (release = r)) }
    setup(server, true)
    await waitFor(() => expect(items()).toEqual(['a']))
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    await waitFor(() => expect(items()).toEqual(['a', 'b']))
    expect(screen.getByTestId('phase')).toHaveTextContent('pending')
    expect(screen.getByTestId('vars')).toHaveTextContent('b')
    release()
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('saved'))
    expect(items()).toEqual(['a', 'b']) // refetched from the server, which now has it
    await waitFor(() => expect(document.querySelector('[data-announcer="polite"]')).toHaveTextContent('Added b.'))
  })

  it('rolls the cache back on failure, keeps the variables, announces assertively and retries the same value', async () => {
    const server = { items: ['a'], fail: true }
    setup(server, true)
    await waitFor(() => expect(items()).toEqual(['a']))
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('failed'))
    expect(items()).toEqual(['a'])
    expect(screen.getByTestId('err')).toHaveTextContent('server said no')
    expect(screen.getByTestId('vars')).toHaveTextContent('b')
    await waitFor(() => expect(document.querySelector('[data-announcer="assertive"]')).toHaveTextContent('Not saved: server said no'))
    server.fail = false
    await userEvent.click(screen.getByRole('button', { name: 'retry' }))
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('saved'))
    expect(items()).toEqual(['a', 'b'])
    expect(server.items).toEqual(['a', 'b'])
  })

  it('without apply nothing is shown as changed before the server confirms', async () => {
    let release!: () => void
    const server = { items: ['a'], fail: false, gate: new Promise<void>((r) => (release = r)) }
    setup(server, false)
    await waitFor(() => expect(items()).toEqual(['a']))
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('pending'))
    expect(items()).toEqual(['a'])
    release()
    await waitFor(() => expect(items()).toEqual(['a', 'b']))
  })

  it('ignores a second save while one is pending, and discard clears a failure', async () => {
    let release!: () => void
    const server = { items: [] as List, fail: false, gate: new Promise<void>((r) => (release = r)) }
    setup(server, false)
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    release()
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('saved'))
    expect(server.items).toEqual(['b'])
  })

  it('discard returns a failed save to idle', async () => {
    setup({ items: [], fail: true }, false)
    await userEvent.click(screen.getByRole('button', { name: 'add' }))
    await waitFor(() => expect(screen.getByTestId('phase')).toHaveTextContent('failed'))
    await userEvent.click(screen.getByRole('button', { name: 'discard' }))
    expect(screen.getByTestId('phase')).toHaveTextContent('idle')
  })
})
