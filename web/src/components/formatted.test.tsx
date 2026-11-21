import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Amount, DateText } from '@/components/formatted'
import { currencyName, formatAmount, formatDate, isIsoDate } from '@/lib/format'

describe('formatting (display only)', () => {
  const NNBSP = String.fromCharCode(0x202f)
  it('groups the digits of an exact decimal string with a fixed narrow no-break space, whatever the locale', () => {
    expect(formatAmount('1124.19')).toBe(`1${NNBSP}124.19`)
    expect(formatAmount('3686.68')).toBe(`3${NNBSP}686.68`)
    expect(formatAmount('1234567.5')).toBe(`1${NNBSP}234${NNBSP}567.5`)
    expect(formatAmount('999.99')).toBe('999.99')
    expect(formatAmount('40')).toBe('40')
    expect(formatAmount('0.10')).toBe('0.10')
    expect(formatAmount('-5000.50')).toBe(`-5${NNBSP}000.50`)
    // More digits than a double holds: nothing is rounded.
    expect(formatAmount('12345678901234567890.123456789').replace(/\D/g, '')).toBe('12345678901234567890123456789')
  })
  it('returns text that is not a decimal unchanged', () => {
    expect(formatAmount('n/a')).toBe('n/a')
    expect(formatAmount('1e3')).toBe('1e3')
  })
  it('shows a calendar date as ISO and validates real dates', () => {
    expect(formatDate('2026-11-01')).toBe('2026-11-01')
    expect(formatDate('soon')).toBe('soon')
    expect(isIsoDate('2026-02-28')).toBe(true)
    expect(isIsoDate('2026-02-30')).toBe(false)
    expect(isIsoDate('11/21/2026')).toBe(false)
    expect(isIsoDate('2026-1-5')).toBe(false)
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
    expect(document.querySelector('time')).toHaveTextContent('2026-11-07')
    expect(document.querySelector('time')).toHaveClass('whitespace-nowrap')
  })
})
