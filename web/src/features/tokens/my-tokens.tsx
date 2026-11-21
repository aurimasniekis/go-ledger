import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { api } from '@/lib/auth'
import { usePagedList } from '@/lib/paged'
import type { ApiToken, CreatedApiToken } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/confirm-dialog'
import { QueryState } from '@/components/query-state'
import { ShowOnceDialog } from '@/components/show-once-dialog'
import { CreateTokenDialog } from '@/features/tokens/create-token-dialog'
import { TokenTable } from '@/features/tokens/token-table'

export function MyTokensPage() {
  const qc = useQueryClient()
  const list = usePagedList<ApiToken>(['my-tokens'], api('/auth/tokens'))
  const [creating, setCreating] = useState(false)
  // The secret lives only here, only while its dialog is open; Done drops it for good.
  const [created, setCreated] = useState<CreatedApiToken | null>(null)
  const [revoking, setRevoking] = useState<ApiToken | null>(null)

  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold">API tokens</h1>
        <Button id="create-token-button" onClick={() => setCreating(true)}>Create token</Button>
      </div>
      <p className="text-sm text-muted-foreground">
        Tokens let a script or tool use the API as you, within the grants you give them and never beyond your own role.
        Requests made with a token skip two-factor authentication and password checks; each change it makes is audited.
        A token is shown only when it is created.
      </p>
      <QueryState query={list} label="your tokens">
        {(tokens) => (
          <>
            <TokenTable tokens={tokens} caption="Your API tokens" onRevoke={setRevoking} />
            {list.hasNextPage && (
              <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                {list.isFetchingNextPage ? 'Loading…' : 'Load more tokens'}
              </Button>
            )}
          </>
        )}
      </QueryState>

      <CreateTokenDialog
        open={creating}
        onOpenChange={setCreating}
        onCreated={(c) => {
          setCreating(false)
          setCreated(c)
          void qc.invalidateQueries({ queryKey: ['my-tokens'] })
        }}
      />
      {created && (
        <ShowOnceDialog
          title={`Token “${created.token.name}” created`}
          description="Copy the token now and keep it somewhere safe."
          lines={[created.secret]}
          listLabel="API token"
          returnFocusId="create-token-button"
          warning={created.warning}
          filename={`ledger-token-${created.token.id}.txt`}
          onDone={() => setCreated(null)}
        />
      )}
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(o) => !o && setRevoking(null)}
        title={`Revoke “${revoking?.name ?? ''}”?`}
        description="Anything using this token stops working at once. This cannot be undone; create a new token if you need one again."
        confirmLabel="Revoke token"
        onConfirm={async () => {
          await apiFetch(api(`/auth/tokens/${revoking!.id}`), { method: 'DELETE' })
          await qc.invalidateQueries({ queryKey: ['my-tokens'] })
        }}
      />
    </section>
  )
}
