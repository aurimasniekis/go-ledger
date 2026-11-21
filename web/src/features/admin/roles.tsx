import { useQuery } from '@tanstack/react-query'
import { apiFetch } from '@/lib/api'
import { api } from '@/lib/auth'
import type { GrantVocabulary } from '@/lib/types'
import { Mono } from '@/components/mono'
import { QueryState } from '@/components/query-state'

/** The roles and the grant vocabulary. Roles are fixed by the server (ADR-0021), so this screen only explains them. */
export function RolesPage() {
  const q = useQuery({ queryKey: ['grants'], queryFn: () => apiFetch<GrantVocabulary>(api('/grants')) })
  return (
    <section className="space-y-4">
      <h1 className="text-2xl font-semibold">Roles</h1>
      <p className="text-sm text-muted-foreground">
        A user has exactly one role. Roles are built in: administrators hold every grant, read-only users hold read on
        everything that is not administrative. API tokens can hold at most what their owner's role holds.
      </p>
      <QueryState query={q} label="roles">
        {(v) => (
          <div className="space-y-6">
            {v.roles.map((r) => (
              <div key={r.name} className="space-y-2" role="group" aria-labelledby={`role-${r.name}`}>
                <h2 id={`role-${r.name}`} className="text-lg font-semibold">
                  {r.name}
                </h2>
                <ul className="flex flex-wrap gap-1">
                  {r.grants.map((g) => (
                    <li key={g}>
                      <Mono>{g}</Mono>
                    </li>
                  ))}
                </ul>
              </div>
            ))}
            <div className="space-y-2">
              <h2 className="text-lg font-semibold">Grant vocabulary</h2>
              <dl className="space-y-2 text-sm">
                {v.resources.map((r) => (
                  <div key={r.name}>
                    <dt className="font-medium">
                      <Mono>{r.name}</Mono> ({r.actions.join(', ')}){r.admin_only && ' · administrative'}
                    </dt>
                    <dd className="text-muted-foreground">{r.description}</dd>
                  </div>
                ))}
              </dl>
            </div>
          </div>
        )}
      </QueryState>
    </section>
  )
}
