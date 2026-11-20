import { Button } from '@/components/ui/button'
import { Mono } from '@/components/mono'
import { COMMITMENT_STATUSES, DATA_STATES, STATUSES, StatusBadge, type Status } from '@/components/status-badge'
import { TOKEN_GROUPS } from '@/lib/tokens'

// /design: the token and component showcase. It shows nothing but static samples.
export function DesignPage() {
  return (
    <div className="space-y-8">
      <header className="space-y-2">
        <h1 className="text-2xl font-semibold">Design system</h1>
        <p className="text-muted-foreground">
          Semantic tokens for the light and dark themes. Switch the theme in the header; every swatch follows it. See{' '}
          <Mono>docs/design-system.md</Mono>.
        </p>
      </header>

      {TOKEN_GROUPS.map((g) => (
        <section key={g.title} aria-labelledby={`tok-${g.id}`} className="space-y-3">
          <h2 id={`tok-${g.id}`} className="text-lg font-semibold">
            {g.title}
          </h2>
          <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
            {g.tokens.map((t) => (
              <li key={t.name} className="flex items-center gap-3 rounded-md border border-border bg-surface p-2 shadow-sm">
                <span aria-hidden className={`size-8 shrink-0 rounded-sm border border-border-control ${t.swatch}`} />
                <span className="min-w-0">
                  <Mono>--{t.name}</Mono>
                  <span className="block text-xs text-muted-foreground">{t.note}</span>
                </span>
              </li>
            ))}
          </ul>
        </section>
      ))}

      <section aria-labelledby="tok-status-badges" className="space-y-3">
        <h2 id="tok-status-badges" className="text-lg font-semibold">
          Status badges
        </h2>
        <p className="text-sm text-muted-foreground">
          Icon and label always travel together; colour is reinforcement only. Emphasis tiers: strong (solid), default
          (tint), quiet (plain). Commitment status and data state are separate axes.
        </p>
        {(
          [
            ['Commitment status', COMMITMENT_STATUSES],
            ['Data state', DATA_STATES],
          ] as const
        ).map(([title, group]) => (
          <div key={title} className="space-y-2">
            <h3 className="text-sm font-semibold">{title}</h3>
            <ul className="flex flex-wrap gap-2">
              {(Object.keys(group) as Status[]).map((s) => (
                <li key={s}>
                  <StatusBadge status={s} />{' '}
                  <span className="text-xs text-muted-foreground">{STATUSES[s].tier}</span>
                </li>
              ))}
            </ul>
          </div>
        ))}
      </section>

      <section aria-labelledby="tok-controls" className="space-y-3">
        <h2 id="tok-controls" className="text-lg font-semibold">
          Controls, radii and elevation
        </h2>
        <div className="flex flex-wrap items-center gap-2">
          <Button>Primary</Button>
          <Button variant="secondary">Secondary</Button>
          <Button variant="outline">Outline</Button>
          <Button variant="ghost">Ghost</Button>
        </div>
        <div className="flex flex-wrap gap-3">
          <div className="rounded-sm border border-border bg-surface p-3 shadow-sm">radius 4px, elevation 1</div>
          <div className="rounded-md border border-border bg-surface-raised p-3 shadow-md">radius 6px, elevation 2</div>
          <div className="rounded-lg border border-border bg-surface p-3">radius 8px, flat</div>
        </div>
        <p>
          Identifier: <Mono>tx_01HZX4K9</Mono>
        </p>
      </section>
    </div>
  )
}
