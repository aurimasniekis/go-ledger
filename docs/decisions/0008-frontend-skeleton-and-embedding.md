# ADR-0008: Frontend skeleton and embedding

Status: Accepted (2026-11-20)

## Context

Ledger is one Go process that serves the Management API and the web UI (ADR-0001). F7 adds the UI project and the way the binary carries it. The stack is fixed by the brief: React, TypeScript (strict) and Vite; TanStack Router and Query; Tailwind CSS; shadcn/ui on Radix; pnpm with a committed lockfile; ESLint, Vitest with Testing Library, Playwright. ADR-0007 left the CI `frontend` job as a placeholder, and Corepack is not shipped with Node 25 and later.

## Decision

1. **Layout.** The source lives in `web/`. `vite build` writes into `internal/web/dist/`, which `internal/web` embeds with `//go:embed all:dist`. Only `internal/web/dist/.gitkeep` is tracked (a small Vite plugin restores it, because `emptyOutDir` deletes it).
2. **Go builds without a frontend build.** Because the placeholder keeps `go:embed` valid, `go build`, `go vet`, golangci-lint and `go test` work on a fresh checkout and the Go CI jobs need no Node. Handler tests use `fstest.MapFS`, not the real build. If the frontend was not built, page requests get a 503 in the error envelope that says the UI is not built; the API is unaffected. The alternative, committing a placeholder `index.html`, would show as a modified tracked file after every build, and building the frontend first in every Go job would slow all of them. The jobs that must prove the real thing, `go-build` and `make build-full`, build the frontend first and smoke-test the binary.
3. **Serving** (`internal/web.Handler`, wired with `httpapi.WithUI`):
   - An existing file is served as is. Files under `assets/` carry a content hash and get `Cache-Control: public, max-age=31536000, immutable`. Every other file, `index.html` included, gets `no-cache` and a strong ETag, so a reload revalidates and a new release is picked up at once.
   - A path that names no file and looks like a page route (no extension, not under `assets/`) gets `index.html` with 200, so deep links and reloads reach the client router.
   - A missing asset (under `assets/` or with an extension) is a 404 in the JSON envelope, never `index.html`, so a stale hashed file does not parse HTML as script.
   - `/api` and `/api/...` never reach the UI: `httpapi.New` registers an `/api/` catch-all (404 envelope, including the request ID) before the UI fallback, and the UI handler refuses `api` paths as well. Specific API routes registered later take precedence over the catch-all.
   - Only GET and HEAD are served; other methods get a 405 envelope.
   - UI responses carry `X-Content-Type-Options: nosniff`, and `index.html` also a Content-Security-Policy (scripts from self only, no inline script; styles allow `'unsafe-inline'` because React and Radix set style attributes), `X-Frame-Options: DENY` and a same-origin referrer policy. The theme bootstrap is therefore a separate file, `theme-init.js`. Wider security headers (HSTS, cookie policy) belong to the authentication task.
4. **Client state.** TanStack Query owns server state: there is no client store that copies it. The only client-only state in the skeleton, the theme choice, is kept in `localStorage` and a `data-theme` attribute. TanStack Store is not installed; it is added when state is genuinely shared between components and is not server data.
5. **Routing.** Code-based TanStack Router routes (`src/router.tsx`), with the query client in the router context and a not-found page. File-based routing and its generator are not needed at this size and can replace it later.
6. **Design tokens.** `src/index.css` defines semantic CSS variables for a Night Owl-inspired dark theme and a designed light theme, with system, light and dark choices, and maps them into Tailwind. A unit test checks the WCAG AA text-contrast pairs in both themes. Reduced motion is honoured. This is the token scaffold only; the visual design comes with the UI tasks. Text for the Night Owl preset beyond the recorded direction block was not available, so no further detail was invented.
7. **shadcn/ui.** `components.json` is committed (new-york style, CSS variables, lucide icons, `@/` aliases) and components are copied into `src/components/ui`, starting with Button, adapted to the tokens. The CLI is not run in CI.
8. **Versions.** Versions were read from the npm registry on 2026-11-20 and pinned exactly in `web/package.json`, with `pnpm-lock.yaml` committed. TypeScript is 6.0.3, not the newer 7.x: typescript-eslint 8.73 declares support for TypeScript below 6.1, so 7.x would run unsupported. Revisit when typescript-eslint supports it. Four exact versions (`@tanstack/query-core` and `@tanstack/react-query` 5.108.2, `lucide-react` 1.77.0, `tailwind-merge` 3.7.1) are listed in `web/pnpm-workspace.yaml` under `minimumReleaseAgeExclude`, because they were newer than pnpm's release-age window when pinned; drop the exclusions once those versions are old enough. The UI handler also ignores `Range` headers, since the shell needs no partial content. `tsconfig.json` does not use `baseUrl` (deprecated in 6); the `@/*` alias uses `paths` only.
9. **CI.** The `frontend` job runs `pnpm install --frozen-lockfile`, lint, typecheck, test and build (through the `web-*` Make targets). pnpm comes from `pnpm/action-setup` pinned by commit SHA at the exact version in `packageManager`; `npm install --global` was rejected by zizmor's `adhoc-packages` audit and Corepack is absent on Node 25+. Node is pinned to an exact version. This replaces rule 7 of ADR-0007. Dependabot also watches `web/`.
10. **Playwright** is a scaffold: `web/playwright.config.ts` starts the built binary with throwaway synthetic configuration and `web/e2e/shell.spec.ts` checks the shell, a deep link, and the API 404 envelope. `make e2e` runs it. It is not in CI yet: that needs browsers installed on the runner, which is a decision for the CI owner.

## Consequences

- A release build must run `make build-full` (frontend first). A plain `go build` produces a binary whose UI answers 503; `make build` alone does the same. F10 release workflows must use `build-full`.
- `go:embed all:dist` includes files whose names start with `.` or `_`; only `.gitkeep` and Vite output are there.
- The CSP will need revisiting when a component needs something stricter or looser than the current policy.
- The two unresolved UI decisions (preset text, user-management audience) are untouched.
