import { beforeEach, describe, expect, it } from 'vitest'
import { applyThemePreference, getThemePreference } from '@/lib/theme'

describe('theme preference', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
  })

  it('defaults to system', () => {
    expect(getThemePreference()).toBe('system')
  })

  it('stores and applies an explicit choice, and clears it for system', () => {
    applyThemePreference('dark')
    expect(document.documentElement.dataset.theme).toBe('dark')
    expect(getThemePreference()).toBe('dark')
    applyThemePreference('system')
    expect(document.documentElement.dataset.theme).toBeUndefined()
    expect(getThemePreference()).toBe('system')
  })
})
