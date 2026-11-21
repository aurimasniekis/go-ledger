import type { SignInMethods } from '@/lib/types'
import { apiFetch } from '@/lib/api'
import { api } from '@/lib/auth'

/** Sends the browser to another address (the identity provider). A function so tests can replace it. */
export const navigation = {
  goTo(url: string) {
    window.location.assign(url)
  },
}

/** What the sign-in page offers. When the answer cannot be read, passwords are assumed (the safe default). */
export const FALLBACK_METHODS: SignInMethods = { password: { enabled: true }, oidc: { enabled: false, name: null, authorize_url: null } }
export const methodsQuery = {
  queryKey: ['sign-in-methods'],
  queryFn: () => apiFetch<SignInMethods>(api('/auth/methods')),
  staleTime: 60_000,
  retry: false,
}

const OIDC_ERRORS: Record<string, string> = {
  not_configured: 'Single sign-on is not set up on this Ledger.',
  provider_unavailable: 'The identity provider could not be reached. Try again in a little while.',
  invalid_state: 'That sign-in attempt expired or was not started here. Start again.',
  access_denied: 'The identity provider did not allow the sign-in.',
  invalid_response: 'The identity provider sent an answer Ledger could not accept. Start again.',
  not_linked: 'No Ledger account is linked to that identity. Sign in another way and link it from Account security.',
  sign_in_failed: 'Sign-in failed. Check with whoever runs this Ledger.',
  already_linked: 'That identity is already linked to an account.',
  session_ended: 'Your Ledger session ended before the provider answered. Sign in and try again.',
  identity_mismatch: 'You signed in at the provider as a different identity than the one linked. Nothing was changed.',
  internal_error: 'Something went wrong on this Ledger. Try again.',
  rate_limited: 'Too many sign-in attempts from your address. Wait a minute and try again.',
}

/** The message for an oidc_error code; unknown codes get a generic line, never the raw text. */
export function oidcErrorMessage(code: string): string {
  return OIDC_ERRORS[code] ?? 'Single sign-on failed. Try again.'
}

export function recoveryErrorMessage(code: string): string {
  return code === 'invalid_or_expired'
    ? 'That recovery link is invalid, expired or already used. Ask for a new one on the host with ledger auth recovery-sign-in.'
    : 'The recovery sign-in failed.'
}
