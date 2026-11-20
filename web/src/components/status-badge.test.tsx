import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { COMMITMENT_STATUSES, DATA_STATES, STATUSES, StatusBadge, type Status } from '@/components/status-badge'

describe('StatusBadge', () => {
  for (const s of Object.keys(STATUSES) as Status[]) {
    it(`${s} shows a text label and a decorative icon, never colour alone`, () => {
      const { container } = render(<StatusBadge status={s} />)
      expect(screen.getByText(STATUSES[s].label)).toBeInTheDocument()
      expect(container.querySelector('svg[aria-hidden="true"]')).not.toBeNull()
    })
  }
  it('uses a distinct icon per status', () => {
    const icons = new Set(Object.values(STATUSES).map((x) => x.Icon))
    expect(icons.size).toBe(Object.keys(STATUSES).length)
  })
  it('orders emphasis strong > default > quiet as documented', () => {
    const tier = (s: Status) => STATUSES[s].tier
    expect(['overdue', 'error'].map((s) => tier(s as Status))).toEqual(['strong', 'strong'])
    expect(['upcoming', 'stale'].map((s) => tier(s as Status))).toEqual(['default', 'default'])
    expect(['paid', 'deferred'].map((s) => tier(s as Status))).toEqual(['quiet', 'quiet'])
    const { container } = render(<StatusBadge status="overdue" />)
    expect(container.querySelector('[data-tier="strong"]')?.className).toContain('bg-status-overdue')
  })
  it('keeps commitment status and data state as separate axes', () => {
    for (const k of Object.keys(DATA_STATES)) expect(k in COMMITMENT_STATUSES).toBe(false)
    expect(Object.keys(STATUSES).length).toBe(Object.keys(COMMITMENT_STATUSES).length + Object.keys(DATA_STATES).length)
  })
})
