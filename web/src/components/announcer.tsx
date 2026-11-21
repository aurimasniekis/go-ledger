import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react'

export type Politeness = 'polite' | 'assertive'
export type Announce = (message: string, politeness?: Politeness) => void

const AnnounceContext = createContext<Announce>(() => {})

/**
 * Speaks a message to screen-reader users without moving focus. Two live regions stay mounted for
 * the life of the shell (a region added together with its text is often not announced). They carry
 * aria-live and no role, so they never count as the page's "alert" or "status":
 * "polite" for outcomes (saved, month changed) and "assertive" for failures and lost connection.
 * Use it for the result of something the user did; text that belongs to a form or panel stays in
 * that panel with its own role. Outside a provider it does nothing, so components stay testable.
 */
export function AnnouncerProvider({ children }: { children: ReactNode }) {
  const [text, setText] = useState<Record<Politeness, string>>({ polite: '', assertive: '' })
  const flip = useRef({ polite: false, assertive: false })
  const announce = useCallback<Announce>((message, politeness = 'polite') => {
    // Clear first, then set, and alternate a trailing no-break space, so repeating the same message is announced again.
    flip.current[politeness] = !flip.current[politeness]
    const next = message + (flip.current[politeness] ? '\xA0' : '')
    setText((t) => ({ ...t, [politeness]: '' }))
    setTimeout(() => setText((t) => ({ ...t, [politeness]: next })), 50)
  }, [])
  return (
    <AnnounceContext.Provider value={announce}>
      {children}
      <div className="sr-only">
        <div aria-live="polite" aria-atomic="true" data-announcer="polite">
          {text.polite}
        </div>
        <div aria-live="assertive" aria-atomic="true" data-announcer="assertive">
          {text.assertive}
        </div>
      </div>
    </AnnounceContext.Provider>
  )
}

export function useAnnounce(): Announce {
  return useContext(AnnounceContext)
}
