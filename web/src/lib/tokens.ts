// Token catalogue for the /design page and the contrast tests' coverage check.
// Swatch classes are written out in full so Tailwind sees them.
export interface TokenInfo {
  name: string
  swatch: string
  note: string
}
export const TOKEN_GROUPS: { id: string; title: string; tokens: TokenInfo[] }[] = [
  {
    id: 'surfaces',
    title: 'Surfaces and text',
    tokens: [
      { name: 'background', swatch: 'bg-background', note: 'Page background' },
      { name: 'surface', swatch: 'bg-surface', note: 'Panels and cards' },
      { name: 'surface-raised', swatch: 'bg-surface-raised', note: 'Popovers, raised panels' },
      { name: 'muted', swatch: 'bg-muted', note: 'Subtle fills' },
      { name: 'foreground', swatch: 'bg-foreground', note: 'Body text' },
      { name: 'muted-foreground', swatch: 'bg-muted-foreground', note: 'Secondary text' },
    ],
  },
  {
    id: 'borders',
    title: 'Borders and focus',
    tokens: [
      { name: 'border', swatch: 'bg-border', note: 'Decorative dividers (not a control boundary)' },
      { name: 'border-control', swatch: 'bg-border-control', note: 'Form-control boundaries, at least 3:1' },
      { name: 'ring', swatch: 'bg-ring', note: 'Focus indicator, at least 3:1' },
    ],
  },
  {
    id: 'accents',
    title: 'Accents',
    tokens: [
      { name: 'primary', swatch: 'bg-primary', note: 'Primary actions (teal)' },
      { name: 'accent-teal', swatch: 'bg-accent-teal', note: 'Teal' },
      { name: 'accent-cyan', swatch: 'bg-accent-cyan', note: 'Cyan' },
      { name: 'accent-violet', swatch: 'bg-accent-violet', note: 'Violet' },
      { name: 'accent-warm', swatch: 'bg-accent-warm', note: 'Warm' },
    ],
  },
  {
    id: 'status',
    title: 'Status',
    tokens: [
      { name: 'status-paid', swatch: 'bg-status-paid', note: 'Text and icon colour; pair with a label' },
      { name: 'status-upcoming', swatch: 'bg-status-upcoming', note: 'Text and icon colour; pair with a label' },
      { name: 'status-overdue', swatch: 'bg-status-overdue', note: 'Text and icon colour; pair with a label' },
      { name: 'status-deferred', swatch: 'bg-status-deferred', note: 'Text and icon colour; pair with a label' },
      { name: 'status-stale', swatch: 'bg-status-stale', note: 'Text and icon colour; pair with a label' },
      { name: 'status-error', swatch: 'bg-status-error', note: 'Text and icon colour; pair with a label' },
      { name: 'status-strong-foreground', swatch: 'bg-status-strong-foreground', note: 'Label on the solid fill of strong badges (overdue, error)' },
    ],
  },
]
