# Ledger web UI

React, TypeScript (strict), Vite, TanStack Router and Query, Tailwind CSS v4, shadcn/ui on Radix. See [ADR-0008](../docs/decisions/0008-frontend-skeleton-and-embedding.md).

```sh
pnpm install --frozen-lockfile   # pnpm 12.21.0, see packageManager
pnpm dev          # Vite dev server; proxies /api to a local `ledger serve` on 127.0.0.1:8080
pnpm lint
pnpm typecheck
pnpm test         # Vitest + Testing Library, offline
pnpm build        # writes ../internal/web/dist, which the Go binary embeds
pnpm test:e2e     # Playwright; needs the built binary: make e2e (run `pnpm exec playwright install chromium` once)
```

Server state belongs in TanStack Query only. Do not copy it into a client store.
