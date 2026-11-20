import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createMemoryHistory } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'
import { App } from '@/App'

describe('App shell', () => {
  it('renders the overview at /', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/'] })} />)
    expect(await screen.findByRole('heading', { level: 1, name: 'Ledger' })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeInTheDocument()
  })

  it('renders a not-found page for an unknown route', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/nope'] })} />)
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument()
  })

  it('lets the theme be chosen with the keyboard-reachable select', async () => {
    render(<App history={createMemoryHistory({ initialEntries: ['/'] })} />)
    await userEvent.selectOptions(await screen.findByLabelText('Theme'), 'dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
  })
})
