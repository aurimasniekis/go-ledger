import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { item, payment } from './fixtures'
import { ProvenanceMarker, provenanceOf } from './provenance'

describe('provenance', () => {
  it('derives bank, manual or both from the per-kind evidence counts and override from an applied hand-set status; an override wins', () => {
    expect(provenanceOf(item('a'))).toBeNull()
    expect(provenanceOf(item('a', { payment: payment({ evidence_count: 2, manual_evidence_count: 2 }) }))).toBe('manual')
    expect(provenanceOf(item('a', { payment: payment({ evidence_count: 1, bank_evidence_count: 1 }) }))).toBe('bank')
    expect(provenanceOf(item('a', { payment: payment({ evidence_count: 2, bank_evidence_count: 1, manual_evidence_count: 1 }) }))).toBe('both')
    expect(provenanceOf(item('a', { payment: payment({ evidence_count: 1 }) }))).toBeNull()
    const override = { status: 'paid' as const, reason: 'x', set_at: '2026-11-01T00:00:00Z', applied: true }
    expect(provenanceOf(item('a', { payment: payment({ evidence_count: 1, manual_evidence_count: 1, override }) }))).toBe('override')
    expect(provenanceOf(item('a', { payment: payment({ override: { ...override, applied: false } }) }))).toBeNull()
  })

  it('renders each kind as text with a hint, on neutral tokens (no status hue)', () => {
    for (const [kind, label] of [['bank', 'Bank match'], ['both', 'Bank match and manual payment'], ['manual', 'Manual payment'], ['override', 'Set by hand']] as const) {
      const { unmount } = render(<ProvenanceMarker kind={kind} />)
      const el = screen.getByText(label)
      expect(el).toHaveAttribute('data-provenance', kind)
      expect(el.className).not.toMatch(/status-|warning/)
      unmount()
    }
  })
})
