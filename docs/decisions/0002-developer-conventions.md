# ADR-0002: Developer conventions

Status: Accepted (2026-11-20)

## Context

The repository had no `go.mod`, lint or build configuration, licence, or contribution rules. The plan requires format, lint, test and build checks before a task is reported complete, and Conventional Commits. The preset texts for licence and conventions are not available yet (decision D3).

## Decision

- **Module and toolchain.** Module `github.com/aurimasniekis/go-ledger`. The Go version in `go.mod` is the minimum, and CI and local builds use the installed toolchain. No dependencies are downloaded for the build beyond the module graph.
- **Formatting.** `gofmt -s`, via `make fmt` (rewrite) and `make fmt-check` (fail).
- **Lint.** golangci-lint v2, configured in `.golangci.yml`. It runs the standard linters plus depguard (the boundaries in ADR-0001), errorlint, misspell and unconvert, and gofmt as a formatter. The local version is 2.15.0.
- **Tests.** `go test -race ./...` via `make test`. Tests run offline: no test may need the network, a bank, a model API, a rate API or a real credential. Fixtures are synthetic.
- **Build.** `make build` writes `bin/ledger`. The version comes from `git describe` through `-ldflags`.
- **Commits and branches.** Conventional Commits 1.0.0 (see CONTRIBUTING.md). Work goes on a branch and merges through a pull request. Default branches are never pushed to directly, and shared branches are never force-pushed.
- **Licence.** Apache-2.0, with the standard licence text in `LICENSE`. Per-file headers are not added until the preset texts arrive (D3). Revisit if the preset adds header rules. A `NOTICE` file is added only if a dependency requires one (OP4).
- **Dependencies.** Prefer pure Go, so that builds do not need CGO. The SQLite driver choice is recorded in the task-F5 decision. Each new dependency has its licence recorded.
- **Local secrets.** `.env` and `.env.*` are git-ignored; `.env.example` is committed when configuration lands (F2).
- **Decisions.** A significant tradeoff gets a numbered ADR in `docs/decisions/`, from `0000-template.md`.

## Consequences

- The checks run only on a developer's machine until CI lands (F6). Nothing enforces them on pull requests yet.
- `govulncheck` is not installed on the build node, so the vulnerability scan is not part of `make check`. F6 adds it with a pinned version.
- `make test` uses `-race`, which needs a C toolchain on the machine (CGO). If that is missing, the target fails. The CI job must provide it.
