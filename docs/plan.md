# Ledger plan (living)

Short, in-repo view of the initial build. Updated when a task changes state. The full plan, with dependencies and open decisions, is kept outside this repository.

Last updated: 2026-11-20.

## Done

- **F1 Repository skeleton and conventions**: module, `cmd/ledger`, internal package layout (ADR-0001), Apache-2.0 licence, `.gitignore`, golangci-lint v2 config, Makefile, CONTRIBUTING, ADR log (ADR-0002 conventions).

## Next (first wave)

- **F2 Configuration system**: YAML, environment and `.env` configuration, `.env.example`.
- **F5 SQLite store and migrations**: driver choice recorded as an ADR, WAL, foreign keys, migration harness.
- **F6 CI validation workflow**: pinned actions, read-only permissions, Go and frontend checks, `govulncheck`.
- **F7 Frontend skeleton and embedding**: React, TypeScript, Vite, embedded into `internal/web`.

## Open decisions that affect this work

- **D3** Preset texts (Apache-2.0 header rules, UI, CI, conventions) are not available. The standard licence text and the conventions in ADR-0002 are used until they arrive.
- The remaining decisions (D1-D19) are listed in the full plan. Each one blocks only the tasks it names.
