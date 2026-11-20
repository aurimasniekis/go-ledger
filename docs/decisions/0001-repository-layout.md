# ADR-0001: Repository layout and package boundaries

Status: Accepted (2026-11-20)

## Context

Ledger is one Go process. It serves the Management API and the embedded web UI, and it has to keep three concerns apart: persistence, business rules and HTTP handling. Banking, currency rates, language models and merchant enrichment must stay behind provider boundaries. The repository was empty when this was decided, so the layout is set here.

## Decision

```
cmd/ledger/                    single binary; wires packages together, holds no business logic
internal/store/                persistence: SQLite setup, forward-only migrations, transactions
internal/domain/<name>/        business rules per concept (money, commitment, occurrence, match, outlook, ...)
internal/httpapi/              HTTP only: routing, request decoding, structured errors, middleware
internal/providers/<name>/     provider adapters (banking, rates, models, enrichment), e.g. enablebanking
internal/web/                  embedded frontend assets via go:embed (frontend source arrives with F7)
docs/decisions/                ADRs; docs/plan.md is the short living plan
```

Rules:

1. Dependency direction runs from `httpapi` to `domain`. `store` and the provider adapters implement interfaces that a domain package declares (ports, defined on the consumer side). Only `cmd/ledger` imports everything.
2. Domain packages import no `net/http`, no `database/sql`, and no `httpapi`, `store` or `providers` package. depguard enforces this in `.golangci.yml`, which `make lint` runs.
3. Finance logic imports no provider adapter. It receives a provider through a port it declares.
4. Layering stays shallow. There are no generic `service`, `repository` or `manager` packages, and no package named after a layer. A handler calls a domain package directly, and a domain package calls a store port directly. Add a layer only when a second concrete use needs it.
5. Everything lives under `internal/`. Go packages are not a public API. The public surface is the versioned HTTP Management API, its OpenAPI description, and the Terraform and MCP surfaces built on it.

## Why

- Persistence, business rules and HTTP change for different reasons. Keeping them apart lets the financial rules be tested offline with fakes, and lets storage or HTTP change without touching those rules.
- Shallow boundaries suit one process and one maintainer. Deep layering (controller, service, repository, DAO) adds indirection without a second implementation to justify it.
- Provider adapters keep Enable Banking specifics out of finance logic. The plan requires that finance packages not import a provider, and that tests drive the logic with a fake provider.
- `internal/` avoids committing to a Go API nobody consumes. The Terraform provider and the MCP server speak HTTP.

## Alternatives considered

- **Classic layered tree** (`internal/service`, `internal/repository`, `internal/handler`). Rejected: extra indirection with no second implementation.
- **Feature packages that mix HTTP, SQL and rules.** Rejected: mixes reasons to change, and makes offline rule tests depend on SQLite or HTTP.
- **Top-level `pkg/` for a public Go API.** Rejected for now: there are no Go consumers. Revisit only if one appears.

## Consequences

- The boundary rules need enforcement. depguard in `.golangci.yml` covers the domain, store and httpapi packages. The provider rule is complete only once finance packages exist, so each finance slice must keep it true and add a test where practical.
- Packages with only `doc.go` are placeholders until their slices land. Each slice brings its own tests.
- Moving a package across a boundary needs a new ADR or an update to this one.
- Left open: the exact port interfaces per domain package. Each slice decides them, within the rule that the domain declares and adapters implement.
