import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { api, holds, useMe } from '@/lib/auth'
import { usePagedList } from '@/lib/paged'
import type { ApiToken, User } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { Select } from '@/components/ui/input'
import { Field } from '@/components/field'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { QueryState } from '@/components/query-state'
import { TokenTable } from '@/features/tokens/token-table'

/** Every user's API tokens (tokens:read); revoking needs tokens:write. */
export function AdminTokensPage() {
  const qc = useQueryClient()
  const me = useMe()
  const mayRevoke = holds(me.data?.grants, 'tokens:write')
  const mayListUsers = holds(me.data?.grants, 'users:read')
  const [owner, setOwner] = useState('')
  const users = usePagedList<User>(['users'], api('/users'), { limit: '200' }, { enabled: mayListUsers })
  const list = usePagedList<ApiToken>(['all-tokens'], api('/tokens'), { owner_id: owner || undefined })
  const [revoking, setRevoking] = useState<ApiToken | null>(null)
  const names = new Map((users.data ?? []).map((u) => [u.id, u.username]))

  return (
    <section className="space-y-4">
      <h1 className="text-2xl font-semibold">All API tokens</h1>
      <p className="text-sm text-muted-foreground">
        Every user's tokens, including expired and revoked ones. Tokens themselves are never shown here, only a hint.
      </p>
      {mayListUsers && (
        <Field label="Owner" name="owner" className="max-w-xs">
          {(p) => (
            <Select {...p} value={owner} onChange={(e) => setOwner(e.target.value)}>
              <option value="">Everyone</option>
              {(users.data ?? []).map((u) => (
                <option key={u.id} value={u.id}>
                  {u.username}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
      <QueryState query={list} label="tokens">
        {(tokens) => (
          <>
            <TokenTable
              tokens={tokens}
              caption="All API tokens"
              ownerLabel={(id) => names.get(id) ?? id}
              onRevoke={mayRevoke ? setRevoking : undefined}
            />
            {list.hasNextPage && (
              <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                {list.isFetchingNextPage ? 'Loading…' : 'Load more tokens'}
              </Button>
            )}
          </>
        )}
      </QueryState>
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={`Revoke “${revoking?.name ?? ''}”?`}
        description={`Anything using this token stops working at once, whoever owns it${revoking ? ` (${names.get(revoking.owner_id) ?? revoking.owner_id})` : ''}. This cannot be undone.`}
        confirmLabel="Revoke token"
        onConfirm={async () => {
          await apiFetch(api(`/tokens/${revoking!.id}`), { method: 'DELETE' })
          await qc.invalidateQueries({ queryKey: ['all-tokens'] })
        }}
      />
    </section>
  )
}
