import { NetworkError } from '@/lib/api'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import { DisconnectedState, EmptyState, FailedState, LoadingState, StaleNotice } from '@/components/state-panels'
import { QueryState } from '@/components/query-state'

describe('state panels', () => {
  it('loading is a busy status and takes no focus', () => {
    render(<LoadingState label="the month" />)
    const s = screen.getByRole('status')
    expect(s).toHaveAttribute('aria-busy', 'true')
    expect(s).toHaveTextContent('Loading the month…')
    expect(document.body).toHaveFocus()
  })

  it('empty names itself and offers the next step', () => {
    render(<EmptyState id="e" level={2} title="Nothing here" action={<button>Add one</button>}>Add a thing.</EmptyState>)
    expect(screen.getByRole('heading', { level: 2, name: 'Nothing here' })).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Nothing here' })).toHaveTextContent('Add a thing.')
    expect(screen.getByRole('button', { name: 'Add one' })).toBeInTheDocument()
  })

  it('failed is an alert that says nothing changed, shows the request reference and retries', async () => {
    const retry = vi.fn()
    render(<FailedState title="Could not load" error={new ApiError(500, { message: 'boom', code: 'internal', request_id: 'req_1' })} onRetry={retry} />)
    const a = screen.getByRole('alert')
    expect(a).toHaveTextContent('Could not load')
    expect(a).toHaveTextContent('boom')
    expect(a).toHaveTextContent('Nothing was changed')
    expect(a).toHaveTextContent('req_1')
    await userEvent.click(screen.getByRole('button', { name: /Try again/ }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it('disconnected says what is still on screen and that nothing can be saved', async () => {
    const retry = vi.fn()
    render(<DisconnectedState onRetry={retry} />)
    const a = screen.getByRole('alert')
    expect(a).toHaveTextContent('Disconnected')
    expect(a).toHaveTextContent('Cannot reach the server')
    expect(a).toHaveTextContent('cannot be saved')
    await userEvent.click(screen.getByRole('button', { name: /Try again/ }))
    expect(retry).toHaveBeenCalledOnce()
  })

  it('stale shows the age of the data it keeps, and a different badge when offline', () => {
    const at = Date.UTC(2026, 10, 21, 9, 0)
    const { rerender } = render(<StaleNotice label="sessions" updatedAt={at} onRetry={() => {}} />)
    expect(screen.getByRole('status')).toHaveTextContent('Stale')
    expect(screen.getByRole('status')).toHaveTextContent('Could not refresh sessions')
    expect(document.querySelector('time')).toHaveAttribute('datetime', '2026-11-21T09:00:00.000Z')
    rerender(<StaleNotice label="sessions" updatedAt={at} onRetry={() => {}} offline />)
    expect(screen.getByRole('status')).toHaveTextContent('Disconnected')
  })
})

describe('QueryState', () => {
  const base = { isFetching: false, dataUpdatedAt: Date.UTC(2026, 10, 21, 9, 0), refetch: vi.fn() }
  it('shows the Updated time as ISO UTC, not in the browser locale', () => {
    render(<QueryState label="sessions" query={{ ...base, data: [], error: null, isError: false, isPending: false, isFetching: false }}>{() => null}</QueryState>)
    const t = screen.getByText('2026-11-21 09:00 UTC')
    expect(t.tagName).toBe('TIME')
    expect(t).toHaveAttribute('datetime', '2026-11-21T09:00:00.000Z')
  })

  it('is disconnected, not failed, when the request never reached the server and nothing has loaded', () => {
    render(<QueryState label="sessions" query={{ ...base, data: undefined, error: new NetworkError(), isError: true, isPending: false }}>{() => null}</QueryState>)
    expect(screen.getByRole('alert')).toHaveTextContent('Cannot reach the server')
  })
  it('keeps earlier data and marks it disconnected when a refresh fails offline', () => {
    render(<QueryState label="sessions" query={{ ...base, data: ['x'], error: new NetworkError(), isError: true, isPending: false }}>{(d) => <p>{d[0]}</p>}</QueryState>)
    expect(screen.getByText('x')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Disconnected')
  })
  it('shows an API failure with a retry when nothing has loaded', () => {
    render(<QueryState label="sessions" query={{ ...base, data: undefined, error: new ApiError(500, { message: 'boom' }), isError: true, isPending: false }}>{() => null}</QueryState>)
    expect(screen.getByRole('alert')).toHaveTextContent('Could not load sessions: boom')
    expect(screen.getByRole('button', { name: 'Try again' })).toBeInTheDocument()
  })
})
