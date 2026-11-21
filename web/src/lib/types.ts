// Shapes of the Management API objects this UI uses (internal/httpapi/openapi.yaml is the contract).
export interface Page<T> {
  items: T[]
  next_cursor: string | null
}
export type Role = 'administrator' | 'read-only'

export interface Session {
  id: string
  current: boolean
  created_at: string
  last_seen_at: string
  authenticated_at: string
  idle_expires_at: string
  expires_at: string
  user_agent: string
  address: string
}
export interface CurrentSession {
  user_id: string
  session: Session
  csrf_token: string
  recent_auth_until: string | null
  two_factor: { enabled: boolean; required: boolean; enrollment_required: boolean }
}
export interface SignInMethods {
  password: { enabled: boolean }
  oidc: { enabled: boolean; name: string | null; authorize_url: string | null }
}
export interface OIDCLinkStatus {
  configured: boolean
  linked: boolean
  provider: string | null
  subject: string | null
  email: string | null
  created_at: string | null
  last_used_at: string | null
}
export interface SecondFactorChallenge {
  second_factor_required: true
  challenge: string
  /** Absent when the challenge came from the single-sign-on redirect, which carries only the token. */
  expires_at?: string
  methods: ('totp' | 'recovery_code')[]
}
export interface TwoFactorStatus {
  enabled: boolean
  method: 'totp' | null
  enabled_at: string | null
  recovery_codes_remaining: number
  required: boolean
  pending_enrollment_until: string | null
}
export interface TOTPEnrollment {
  secret: string
  otpauth_uri: string
  expires_at: string
}
export interface RecoveryCodes {
  recovery_codes: string[]
  sessions_ended?: number
}
export interface Principal {
  kind: 'user' | 'token'
  user_id: string
  token_id: string | null
  role: Role
  grants: string[]
}
export interface GrantVocabulary {
  resources: { name: string; description: string; actions: ('read' | 'write')[]; admin_only: boolean }[]
  roles: { name: Role; grants: string[] }[]
}
export interface ApiToken {
  id: string
  name: string
  owner_id: string
  root: boolean
  grants: string[]
  hint: string
  status: 'active' | 'expired' | 'revoked'
  created_at: string
  expires_at: string | null
  last_used_at: string | null
  revoked_at: string | null
}
export interface CreatedApiToken {
  secret: string
  token: ApiToken
  warning: string | null
}
export interface User {
  id: string
  username: string
  display_name: string | null
  role: Role
  disabled: boolean
  disabled_at: string | null
  version: number
  created_at: string
  updated_at: string
}
