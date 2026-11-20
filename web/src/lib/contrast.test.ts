import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// WCAG relative-luminance contrast, checked against the tokens in index.css.
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
function tokens(block: RegExp) {
  const body = block.exec(css)?.[1] ?? ''
  return Object.fromEntries([...body.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{6});/g)].map((m) => [m[1]!, m[2]!]))
}
const themes = {
  light: tokens(/:root \{([^}]*)\}/),
  dark: tokens(/:root\[data-theme='dark'\] \{([^}]*)\}/),
}
const pairs: [string, string][] = [
  ['foreground', 'background'],
  ['foreground', 'surface'],
  ['muted-foreground', 'background'],
  ['muted-foreground', 'muted'],
  ['primary-foreground', 'primary'],
  ['secondary-foreground', 'secondary'],
  ['primary', 'background'],
  ['danger', 'background'],
  ['warning', 'background'],
  ['success', 'background'],
]

describe('theme tokens meet WCAG AA text contrast (4.5:1)', () => {
  for (const [name, t] of Object.entries(themes)) {
    for (const [fg, bg] of pairs) {
      it(`${name}: ${fg} on ${bg}`, () => {
        expect(t[fg], `${fg} token`).toBeDefined()
        expect(ratio(t[fg]!, t[bg]!)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }
  it('the OS-dark media block repeats the dark tokens', () => {
    const m = /prefers-color-scheme: dark\) \{\s*:root:not\(\[data-theme='light'\]\) \{([^}]*)\}/.exec(css)?.[1] ?? ''
    const media = Object.fromEntries([...m.matchAll(/--([a-z-]+):\s*(#[0-9a-f]{6});/g)].map((x) => [x[1]!, x[2]!]))
    for (const [k, v] of Object.entries(themes.dark)) expect(media[k]).toBe(v)
  })
})
