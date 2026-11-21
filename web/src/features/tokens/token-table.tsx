import { Button } from '@/components/ui/button'
import { Mono } from '@/components/mono'
import { formatInstant } from '@/lib/format'
import type { ApiToken } from '@/lib/types'

const STATUS_TEXT: Record<ApiToken['status'], string> = { active: 'Active', expired: 'Expired', revoked: 'Revoked' }

/** A responsive token list. It never holds a token, only the hint the server returns. */
export function TokenTable({
  tokens,
  caption,
  ownerLabel,
  onRevoke,
}: {
  tokens: ApiToken[]
  caption: string
  /** Shows an Owner column when set (administrators' list of every token). */
  ownerLabel?: (ownerId: string) => string
  onRevoke?: (t: ApiToken) => void
}) {
  if (tokens.length === 0) return <p className="text-sm text-muted-foreground">No API tokens yet.</p>
  return (
    <div role="region" aria-label={caption} tabIndex={0} className="overflow-x-auto rounded-md border border-border">
      <table className="w-full min-w-[40rem] text-left text-sm">
        <caption className="sr-only">{caption}</caption>
        <thead className="bg-muted text-xs uppercase text-muted-foreground">
          <tr>
            <th scope="col" className="px-3 py-2">Name</th>
            {ownerLabel && <th scope="col" className="px-3 py-2">Owner</th>}
            <th scope="col" className="px-3 py-2">Token</th>
            <th scope="col" className="px-3 py-2">Grants</th>
            <th scope="col" className="px-3 py-2">Status</th>
            <th scope="col" className="px-3 py-2">Expires</th>
            <th scope="col" className="px-3 py-2">Last used</th>
            {onRevoke && (
              <th scope="col" className="px-3 py-2">
                <span className="sr-only">Actions</span>
              </th>
            )}
          </tr>
        </thead>
        <tbody>
          {tokens.map((t) => (
            <tr key={t.id} className="border-t border-border align-top">
              <th scope="row" className="px-3 py-2 font-medium">{t.name}</th>
              {ownerLabel && <td className="px-3 py-2">{ownerLabel(t.owner_id)}</td>}
              <td className="px-3 py-2"><Mono>{t.hint}</Mono></td>
              <td className="px-3 py-2">
                {t.root ? (
                  <span className="font-semibold text-status-overdue">Root (every grant of its owner)</span>
                ) : (
                  <ul className="flex flex-wrap gap-1">
                    {t.grants.map((g) => (
                      <li key={g}><Mono>{g}</Mono></li>
                    ))}
                  </ul>
                )}
              </td>
              <td className="px-3 py-2">
                {STATUS_TEXT[t.status]}
                {t.revoked_at && <span className="block text-xs text-muted-foreground">{formatInstant(t.revoked_at)}</span>}
              </td>
              <td className="px-3 py-2">{t.expires_at ? formatInstant(t.expires_at) : 'Never'}</td>
              <td className="px-3 py-2">{t.last_used_at ? formatInstant(t.last_used_at) : 'Never'}</td>
              {onRevoke && (
                <td className="px-3 py-2 text-right">
                  {t.status !== 'revoked' && (
                    <Button variant="outline" size="sm" aria-label={`Revoke token ${t.name}`} onClick={() => onRevoke(t)}>
                      Revoke
                    </Button>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
