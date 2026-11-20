import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { STATUSES, StatusBadge, type Status } from '@/components/status-badge'

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
})
