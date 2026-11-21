import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Amount, DateText } from '@/components/formatted'
import { currencyName, formatAmount, formatDate } from '@/lib/format'

describe('formatting (display only)', () => {
  it('groups the digits of an exact decimal string without changing its value or precision', () => {
    expect(formatAmount('1124.19')).toBe(new Intl.NumberFormat(undefined, { minimumFractionDigits: 2 }).format(1124.19))
    expect(formatAmount('1124.19')).toMatch(/1\D?124\D19/)
    expect(formatAmount('40')).toBe('40')
    expect(formatAmount('0.10')).toMatch(/0\D10$/)
    // More digits than a double holds: nothing is rounded.
    expect(formatAmount('12345678901234567890.123456789').replace(/\D/g, '')).toBe('12345678901234567890123456789')
    expect(formatAmount('-5.50')).toMatch(/^-?.*5\D50$/)
  })
  it('returns text that is not a decimal unchanged', () => {
    expect(formatAmount('n/a')).toBe('n/a')
    expect(formatAmount('1e3')).toBe('1e3')
  })
  it('formats a calendar date without shifting the day by the browser zone', () => {
    expect(formatDate('2026-11-01')).toMatch(/2026/)
    expect(formatDate('2026-11-01')).toMatch(/\b1\b/)
    expect(formatDate('2026-12-31')).toMatch(/31/)
    expect(formatDate('soon')).toBe('soon')
  })
  it('names a currency for speech and falls back to the code', () => {
    expect(currencyName('EUR')).toMatch(/euro/i)
    expect(currencyName('NOT-A-CODE')).toBe('NOT-A-CODE')
  })
  it('Amount shows the code to the eye and the currency name to a screen reader', () => {
    const { container } = render(<Amount amount="1124.19" currency="EUR" />)
    expect(container.querySelector('[aria-hidden]')).toHaveTextContent(/EUR$/)
    expect(screen.getByText(/euro/i)).toHaveClass('sr-only')
  })
  it('DateText carries the ISO value', () => {
    render(<DateText value="2026-11-07" />)
    expect(document.querySelector('time')).toHaveAttribute('datetime', '2026-11-07')
  })
})
