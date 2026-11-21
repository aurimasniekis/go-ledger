import { useCallback, useEffect, useRef, useState } from 'react'

const usable = (el: HTMLElement | null | undefined): el is HTMLElement =>
  !!el && el.isConnected && !(el as HTMLButtonElement).disabled && el.getAttribute('aria-hidden') !== 'true'

/** Focuses the first element that can still take focus, else the main region (never BODY). */
export function focusFirstUsable(candidates: (HTMLElement | null | undefined)[]): boolean {
  const target = candidates.find(usable) ?? document.getElementById('main')
  if (!target) return false
  target.focus()
  return document.activeElement === target
}

/**
 * Gives focus back after a transient surface (an in-page form, a notice) closes: to what opened it,
 * else to the fallbacks in order, else to the main region. restore() takes effect after the next
 * render, so a control that was disabled while the surface was open is usable again by then.
 * Call remember() when the surface opens.
 */
export function useFocusRestore(fallbacks: () => (HTMLElement | null | undefined)[] = () => []) {
  const opener = useRef<HTMLElement | null>(null)
  const [tick, setTick] = useState(0)
  const fb = useRef(fallbacks)
  useEffect(() => {
    fb.current = fallbacks
  })
  const remember = useCallback(() => {
    opener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
  }, [])
  const restore = useCallback(() => setTick((n) => n + 1), [])
  useEffect(() => {
    if (tick === 0) return
    focusFirstUsable([opener.current, ...fb.current()])
  }, [tick])
  return { remember, restore }
}
