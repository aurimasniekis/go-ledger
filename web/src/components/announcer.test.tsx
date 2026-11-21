import { act, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AnnouncerProvider, useAnnounce } from '@/components/announcer'

function Host() {
  const announce = useAnnounce()
  return (
    <div>
      <button onClick={() => announce('Saved.')}>say</button>
      <button onClick={() => announce('Not saved: offline', 'assertive')}>fail</button>
    </div>
  )
}

const region = (p: 'polite' | 'assertive') => document.querySelector(`[data-announcer="${p}"]`)!

describe('announcer', () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }))
  afterEach(() => vi.useRealTimers())

  it('speaks through always-mounted live regions without moving focus', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<AnnouncerProvider><Host /></AnnouncerProvider>)
    expect(region('polite')).toHaveAttribute('aria-live', 'polite')
    expect(region('assertive')).toHaveAttribute('aria-live', 'assertive')
    expect(region('polite')).toBeEmptyDOMElement()
    const button = screen.getByRole('button', { name: 'say' })
    await user.click(button)
    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(region('polite')).toHaveTextContent('Saved.')
    expect(button).toHaveFocus()
    await user.click(screen.getByRole('button', { name: 'fail' }))
    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(region('assertive')).toHaveTextContent('Not saved: offline')
    expect(region('polite')).toHaveTextContent('Saved.')
  })

  it('announces the same message again (the region is cleared first)', async () => {
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime })
    render(<AnnouncerProvider><Host /></AnnouncerProvider>)
    await user.click(screen.getByRole('button', { name: 'say' }))
    await act(() => vi.advanceTimersByTimeAsync(100))
    const first = region('polite').textContent
    await user.click(screen.getByRole('button', { name: 'say' }))
    expect(region('polite')).toBeEmptyDOMElement()
    await act(() => vi.advanceTimersByTimeAsync(100))
    expect(region('polite').textContent).not.toBe(first)
    expect(region('polite').textContent?.trim()).toBe('Saved.')
  })

  it('never counts as the page alert or status', () => {
    render(<AnnouncerProvider><Host /></AnnouncerProvider>)
    expect(screen.queryByRole('alert')).toBeNull()
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('does nothing outside a provider', async () => {
    render(<Host />)
    await userEvent.click(screen.getByRole('button', { name: 'say' }))
  })
})
