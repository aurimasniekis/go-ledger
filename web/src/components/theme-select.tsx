import { useState } from 'react'
import { applyThemePreference, getThemePreference, type ThemePreference } from '@/lib/theme'

export function ThemeSelect() {
  const [pref, setPref] = useState<ThemePreference>(getThemePreference)
  return (
    <label className="flex items-center gap-2 text-sm">
      <span>Theme</span>
      <select
        className="h-8 rounded-md border border-border-control bg-surface px-2 focus-visible:outline-2 focus-visible:outline-ring"
        value={pref}
        onChange={(e) => {
          const next = e.target.value as ThemePreference
          setPref(next)
          applyThemePreference(next)
        }}
      >
        <option value="system">System</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select>
    </label>
  )
}
