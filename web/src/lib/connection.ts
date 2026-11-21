import { useSyncExternalStore } from 'react'
import { NetworkError } from '@/lib/api'

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

/** apiFetch wraps a fetch that never reached the server in a NetworkError; a bare TypeError is a programming error and is not "disconnected". */
export function isNetworkError(e: unknown): boolean {
  return e instanceof NetworkError
}
