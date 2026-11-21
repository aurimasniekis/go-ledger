import { Link, useNavigate } from '@tanstack/react-router'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { apiFetch, setCsrfToken } from '@/lib/api'
import { api, holds, useMe, useSession } from '@/lib/auth'
import { Button } from '@/components/ui/button'

const link = 'rounded-md px-2 py-1 hover:bg-muted [&.active]:font-semibold [&.active]:underline'

/** Navigation for the signed-in user: what they may reach, and sign out. Not shown while confined to enrolment except sign out. */
export function AccountNav() {
  const session = useSession()
  const me = useMe()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const out = useMutation({
    mutationFn: () => apiFetch(api('/auth/sign-out'), { method: 'POST' }),
    onSuccess: async () => {
      setCsrfToken(null)
      qc.clear()
      await navigate({ to: '/sign-in' })
    },
  })
  if (!session.data) return null
  const confined = session.data.two_factor.enrollment_required
  const grants = me.data?.grants
  return (
    <>
      {!confined && (
        <>
          <Link to="/account/security" className={link}>Security</Link>
          <Link to="/account/sessions" className={link}>Sessions</Link>
          <Link to="/account/tokens" className={link}>API tokens</Link>
          {holds(grants, 'users:read') && <Link to="/admin/users" className={link}>Users</Link>}
          {holds(grants, 'users:read') && <Link to="/admin/roles" className={link}>Roles</Link>}
          {holds(grants, 'tokens:read') && <Link to="/admin/tokens" className={link}>All tokens</Link>}
        </>
      )}
      <Button variant="outline" size="sm" disabled={out.isPending} onClick={() => out.mutate()}>
        {out.isPending ? 'Signing out…' : 'Sign out'}
      </Button>
      {out.isError && (
        <span role="alert" className="text-xs text-status-error">
          Sign-out failed: {out.error.message}
        </span>
      )}
    </>
  )
}
