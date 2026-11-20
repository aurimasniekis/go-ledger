# Contributing

Ledger is a personal finance application for one maintainer. Changes arrive as pull requests.

## Setup

- Go, as set in `go.mod`.
- `make`.
- golangci-lint v2.15.0, for `make lint`.
- Go modules, downloaded once (`go mod download`). After that, no network access is needed for tests.

## Workflow

1. Branch from `main`. Never push to `main` and never force-push a shared branch.
2. Keep one task or concern per pull request.
3. Run `make check` before you open a pull request. It runs gofmt, vet, lint, the race-enabled tests and the build. Frontend checks join it with the UI (F7).
4. Open a pull request that says what changed, how you verified it, and what you did not verify.
5. Record a significant tradeoff in `docs/decisions/` (see its README).
6. A schema change is a new numbered file in `internal/store/migrations/`, plus a fixture for the previous version in `internal/store/testdata/fixtures/`. Never edit an applied migration (ADR-0003).

## Commit messages

Use [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>(<optional scope>)!: <summary>

<optional body>

<optional footers>
```

- Types: `feat`, `fix`, `docs`, `refactor`, `test`, `build`, `ci`, `perf`, `chore`.
- Summary: imperative, lower case, no full stop.
- A breaking change uses `!` or a `BREAKING CHANGE:` footer.

Examples:

```
feat(domain): add monthly occurrence generation
fix(store): roll back a failed migration
docs: record the package layout in ADR-0001
```

## Rules that always apply

- No secrets, credentials, real bank data or real personal finance records in the repository, in commits, in logs or in test fixtures. Fixtures are synthetic.
- Money uses exact decimal or integer representations with a currency. Do not use `float64` for money. A combination of currencies needs an explicit conversion policy.
- Tests run offline. They use fakes or fixtures in place of banking, rate and model APIs.
- Errors and logs do not carry credentials, bank payloads or stack traces to clients.

## Licence

Ledger is licensed under Apache-2.0 (see `LICENSE`). By contributing you agree that your contribution is licensed the same way.
