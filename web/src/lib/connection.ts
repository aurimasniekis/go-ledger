import { useSyncExternalStore } from 'react'

const subscribe = (cb: () => void) => {
  window.addEventListener('online', cb)
  window.addEventListener('offline', cb)
  return () => {
    window.removeEventListener('online', cb)
    window.removeEventListener('offline', cb)
  }
}

/** False when the browser reports no network. A hint only: a reachable network does not prove the server answers. */
export function useOnline(): boolean {
  return useSyncExternalStore(subscribe, () => navigator.onLine, () => true)
}

/** A fetch that never reached the server (offline, refused, DNS) rejects with a TypeError, not an ApiError. */
export function isNetworkError(e: unknown): boolean {
  return e instanceof TypeError
}
