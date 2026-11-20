// Theme preference (system, light or dark). This is client-only presentation
// state, not server state, so it lives in localStorage and a data attribute.
export type ThemePreference = 'system' | 'light' | 'dark'
const KEY = 'ledger.theme'

export function getThemePreference(): ThemePreference {
  try {
    const v = localStorage.getItem(KEY)
    return v === 'light' || v === 'dark' ? v : 'system'
  } catch {
    return 'system'
  }
}

export function applyThemePreference(pref: ThemePreference) {
  try {
    if (pref === 'system') localStorage.removeItem(KEY)
    else localStorage.setItem(KEY, pref)
  } catch {
    // Storage may be unavailable; the preference then lasts for this page load.
  }
  if (pref === 'system') delete document.documentElement.dataset.theme
  else document.documentElement.dataset.theme = pref
}
