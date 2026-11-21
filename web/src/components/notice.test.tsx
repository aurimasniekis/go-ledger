import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Notice } from './notice'

describe('Notice', () => {
  it('is a note with the warning tokens, never a status hue', () => {
    render(<Notice>Careful.</Notice>)
    const n = screen.getByRole('note')
    expect(n).toHaveTextContent('Careful.')
    expect(n.className).toContain('text-warning')
    expect(n.className).toContain('bg-warning-bg')
    expect(n.className).not.toMatch(/status-/)
  })
  it('can be an alert', () => {
    render(<Notice role="alert">Root.</Notice>)
    expect(screen.getByRole('alert')).toBeInTheDocument()
  })
})
