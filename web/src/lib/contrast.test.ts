import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// WCAG 2.2 relative-luminance contrast, checked against the tokens in index.css.
function lum(hex: string) {
  const c = [1, 3, 5].map((i) => {
    const v = parseInt(hex.slice(i, i + 2), 16) / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * c[0]! + 0.7152 * c[1]! + 0.0722 * c[2]!
}
function ratio(a: string, b: string) {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x)
  return (hi! + 0.05) / (lo! + 0.05)
}

const css = readFileSync(resolve(process.cwd(), 'src/index.css'), 'utf8')
function parse(body: string) {
  return Object.fromEntries([...body.matchAll(/--([a-z0-9-]+):\s*(#[0-9a-f]{6});/g)].map((m) => [m[1]!, m[2]!]))
}
const themes = {
  light: parse(/:root \{([^}]*)\}/.exec(css)?.[1] ?? ''),
  dark: parse(/:root\[data-theme='dark'\] \{([^}]*)\}/.exec(css)?.[1] ?? ''),
}

const STATUS = ['paid', 'upcoming', 'overdue', 'deferred', 'stale', 'error'].map((s) => 'status-' + s)
const ACCENT = ['accent-teal', 'accent-cyan', 'accent-violet', 'accent-warm', 'primary', 'accent']
const SURFACES = ['background', 'surface', 'surface-raised', 'muted']

// Text (and icon, which carries meaning) pairs: 4.5:1.
const textPairs: [string, string][] = [
  ...['background', 'surface', 'surface-raised'].map((s): [string, string] => ['foreground', s]),
  ...SURFACES.map((s): [string, string] => ['muted-foreground', s]),
  ['foreground', 'muted'],
  ['primary-foreground', 'primary'],
  ['secondary-foreground', 'secondary'],
  ...ACCENT.flatMap((a) => SURFACES.map((s): [string, string] => [a, s])),
  ...STATUS.flatMap((s) => [...SURFACES.map((b): [string, string] => [s, b]), [s, s + '-bg'] as [string, string]]),
]
// Non-text pairs (WCAG 1.4.11): control boundaries and focus rings, 3:1.
const uiPairs: [string, string][] = ['border-control', 'ring'].flatMap((t) =>
  SURFACES.map((s): [string, string] => [t, s]),
)
// Tokens that are deliberately exempt from a contrast check, with the reason.
const exempt: Record<string, string> = {
  border: 'decorative divider; a control boundary uses border-control',
  secondary: 'fill, checked as the secondary-foreground pair',
  'primary-foreground': 'checked on primary',
  'secondary-foreground': 'checked on secondary',
  ...Object.fromEntries(STATUS.map((s) => [s + '-bg', 'tint, checked as the background of ' + s])),
}

describe('theme tokens meet WCAG 2.2 AA', () => {
  for (const [name, t] of Object.entries(themes)) {
    for (const [fg, bg] of textPairs) {
      it(`${name}: text ${fg} on ${bg} is at least 4.5:1`, () => {
        expect(t[fg], `${fg} token`).toBeDefined()
        expect(t[bg], `${bg} token`).toBeDefined()
        expect(ratio(t[fg]!, t[bg]!)).toBeGreaterThanOrEqual(4.5)
      })
    }
    for (const [fg, bg] of uiPairs) {
      it(`${name}: non-text ${fg} on ${bg} is at least 3:1`, () => {
        expect(t[fg], `${fg} token`).toBeDefined()
        expect(ratio(t[fg]!, t[bg]!)).toBeGreaterThanOrEqual(3)
      })
    }
    it(`${name}: every colour token is covered by a pair or an explicit exemption`, () => {
      const covered = new Set([...textPairs, ...uiPairs].flat())
      const missing = Object.keys(t).filter((k) => !covered.has(k) && !(k in exempt))
      expect(missing).toEqual([])
    })
  }

  it('both themes define the same tokens', () => {
    expect(Object.keys(themes.light).sort()).toEqual(Object.keys(themes.dark).sort())
  })

  it('the OS-dark media block repeats the dark tokens', () => {
    const m = /prefers-color-scheme: dark\) \{\s*:root:not\(\[data-theme='light'\]\) \{([^}]*)\}/.exec(css)?.[1] ?? ''
    expect(parse(m)).toEqual(themes.dark)
  })

  it('honours reduced motion', () => {
    expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)[\s\S]*transition-duration: 0\.01ms !important/)
  })

  it('keeps radii within 4-8px', () => {
    const px = [...css.matchAll(/--radius(?:-small|-large)?: (\d+)px;/g)].map((m) => Number(m[1]))
    expect(px.length).toBe(3)
    for (const v of px) expect(v >= 4 && v <= 8).toBe(true)
  })
})
