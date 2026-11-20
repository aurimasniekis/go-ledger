import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { TOKEN_GROUPS } from '@/lib/tokens'

const src = resolve(process.cwd(), 'src')
function walk(dir: string): string[] {
  return readdirSync(dir).flatMap((f) => {
    const p = join(dir, f)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
}
const css = readFileSync(join(src, 'index.css'), 'utf8')
const sources = walk(src).filter(
  (f) => /\.(tsx?|css)$/.test(f) && !f.endsWith('index.css') && !/\.test\.tsx?$/.test(f),
)

// One-off colours: hex or functional colours, or default-palette utilities such as bg-red-500, bg-white.
const hex = /#[0-9a-fA-F]{3,8}\b/
const fn = /\b(?:rgb|rgba|hsl|hsla|oklch|oklab|lab|lch|hwb|color)\(/
const palette =
  /\b(?:bg|text|border|ring|outline|fill|stroke|from|to|via|divide|shadow|decoration|caret|accent)-(?:(?:slate|gray|zinc|neutral|stone|red|orange|amber|yellow|lime|green|emerald|teal|cyan|sky|blue|indigo|violet|purple|fuchsia|pink|rose)-\d{2,3}|white|black)\b/

describe('no one-off colours', () => {
  for (const f of sources) {
    it(`${f.slice(src.length + 1)} uses tokens only`, () => {
      const text = readFileSync(f, 'utf8')
      expect(text).not.toMatch(hex)
      expect(text).not.toMatch(fn)
      expect(text).not.toMatch(palette)
    })
  }
  it('the Tailwind default palette and shadows are cleared', () => {
    expect(css).toMatch(/--color-\*: initial;/)
    expect(css).toMatch(/--shadow-\*: initial;/)
  })
  it('the detector rejects what it should', () => {
    expect(hex.test('x #fff')).toBe(true)
    expect(fn.test('rgb(0 0 0)')).toBe(true)
    expect(palette.test('bg-red-500')).toBe(true)
    expect(palette.test('text-status-paid bg-surface')).toBe(false)
  })
})

describe('token catalogue', () => {
  it('lists every colour token defined in the theme', () => {
    const body = /:root \{([^}]*)\}/.exec(css)?.[1] ?? ''
    const defined = [...body.matchAll(/--([a-z0-9-]+):\s*#/g)].map((m) => m[1]!)
    const listed = new Set(TOKEN_GROUPS.flatMap((g) => g.tokens.map((t) => t.name)))
    const unlisted = defined.filter(
      (n) => !listed.has(n) && !['primary-foreground', 'secondary', 'secondary-foreground', 'accent'].includes(n) && !n.endsWith('-bg'),
    )
    expect(unlisted).toEqual([])
  })
})
