// The session's CSRF token (ADR-0026), kept in memory only. The sign-in flow
// calls setCsrfToken with the token the session response carries; writes made
// with the session cookie send it in X-CSRF-Token. It is never persisted or
// logged.
let token = ''

export function setCsrfToken(t: string) {
  token = t
}

/** Headers a state-changing request needs: the CSRF token, when one is known. */
export function csrfHeaders(): Record<string, string> {
  return token ? { 'X-CSRF-Token': token } : {}
}
