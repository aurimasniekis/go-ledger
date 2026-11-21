import { useState } from 'react'
import { holds, useMe } from '@/lib/auth'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/state-panels'
import { QueryState } from '@/components/query-state'
import { Notice } from '@/components/notice'
import { useConnections } from './api'
import { ConnectDialog } from './connect-dialog'
import { ConnectionCard } from './connection-card'

/** Bank connections (B4): what is connected, what needs the user, and how fresh each account's data is. */
export function ConnectionsPage() {
  const list = useConnections()
  const me = useMe()
  const [connecting, setConnecting] = useState(false)
  const canWrite = holds(me.data?.grants, 'connections:write')
  return (
    <section className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h1 className="text-2xl font-semibold">Bank connections</h1>
        {canWrite && (
          <Button id="connect-bank-button" onClick={() => setConnecting(true)}>
            Connect a bank
          </Button>
        )}
      </div>
      <p className="text-sm text-muted-foreground">
        Balances and transactions come from your bank as of the times shown on each account; Ledger cannot show anything newer, and a failed refresh never
        replaces data with zeros. You approve access at your bank: Ledger never asks for your bank login.
      </p>
      {!canWrite && me.data && <Notice>Your role can see connections but not change them.</Notice>}
      <QueryState query={list} label="bank connections">
        {(items) => (
          <>
            {items.length === 0 ? (
              <EmptyState
                id="no-connections"
                title="No bank connected yet"
                action={
                  canWrite ? (
                    <Button className="mt-2" onClick={() => setConnecting(true)}>
                      Connect a bank
                    </Button>
                  ) : undefined
                }
              >
                Connect a bank to see balances and to let Ledger find payments for your commitments. Until then, commitments are tracked from your own records.
              </EmptyState>
            ) : (
              <div className="space-y-4">
                {items.map((c) => (
                  <ConnectionCard key={c.id} c={c} canWrite={canWrite} />
                ))}
              </div>
            )}
            {list.hasNextPage && (
              <Button variant="outline" disabled={list.isFetchingNextPage} onClick={() => void list.fetchNextPage()}>
                {list.isFetchingNextPage ? 'Loading…' : 'Load more connections'}
              </Button>
            )}
          </>
        )}
      </QueryState>
      <ConnectDialog open={connecting} onOpenChange={setConnecting} />
    </section>
  )
}
