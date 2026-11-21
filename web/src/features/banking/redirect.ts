/**
 * Sends the browser to the bank's (or provider's) approval page. Only an https address is followed
 * (plain http only on loopback, for a local test bank); anything else is refused, so a bad answer can
 * never send the user somewhere unexpected. A separate module so tests can replace it.
 */
export function isSafeBankUrl(raw: string): boolean {
  try {
    const u = new URL(raw)
    return u.protocol === 'https:' || (u.protocol === 'http:' && ['localhost', '127.0.0.1', '[::1]'].includes(u.hostname))
  } catch {
    return false
  }
}

export function goToBank(url: string): void {
  if (!isSafeBankUrl(url)) throw new Error('The bank address Ledger received is not a secure web address, so it was not opened.')
  window.location.assign(url)
}
