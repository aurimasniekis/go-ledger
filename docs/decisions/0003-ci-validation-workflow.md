# ADR-0003: CI validation workflow

Status: Accepted (2026-11-20)

## Context

ADR-0002 left the checks to developers' machines until the CI validation workflow (F6) landed. F6 must run Go and frontend checks on pushes and pull requests, scan dependencies, and enforce the operator's CI/CD requirements: a top-level `permissions: contents: read`, third-party actions pinned, and no job with write permissions. Release and publishing workflows are a separate task (F10). The frontend does not exist yet (F7), so its checks must not fail the pipeline before then.

## Decision

1. One workflow, `.github/workflows/ci.yml`, runs on every push and on every pull request. Its top-level `permissions` is `contents: read`. No job requests a write scope.
2. Every `uses:` is pinned to a full commit SHA, with the release name in a trailing comment. `.github/dependabot.yml` proposes pin updates as pull requests.
3. CI runs the same Makefile targets that developers run, so `make check` and CI cannot drift: `fmt-check`, `vet`, `lint`, `test` (race) and `build`. `govulncheck ./...` runs as its own job.
4. Tools are pinned by version and installed directly, not through third-party actions: golangci-lint v2.15.0, govulncheck v1.10.0 and actionlint v1.7.12 with `go install`; zizmor 1.32.0 from PyPI with `--only-binary`.
5. The `workflow-lint` job runs actionlint and zizmor (`--offline`, low severity and up). A permission-policy step fails when any workflow requests a write scope or lacks a top-level `contents: read`. zizmor's `unpinned-uses` audit fails any unpinned action.
6. Dependency review runs on pull requests only, with `fail-on-severity: moderate` and `contents: read`; it does not post comments.
7. The `frontend` job runs `pnpm lint`, `typecheck`, `test` and `build` in `web/` with a frozen lockfile. It runs only when `web/package.json` exists; otherwise it passes with a notice.

## Why

- Direct `go install` and `pip` pins keep the set of third-party actions to four official GitHub actions (checkout, setup-go, setup-node, dependency-review). The alternatives were golangci-lint-action and a zizmor action: each adds another action to pin and another install path that differs from `make lint`.
- zizmor's `unpinned-uses` audit enforces the pin rule in a way a hand-written script would not, and it also covers `persist-credentials` and injection risks. actionlint checks syntax and expressions. Both were run against a fixture with an unpinned action; zizmor failed it (exit 14) and the real workflow passes.
- zizmor's defaults do not flag a job-level `contents: write` (it suppressed the finding in a test fixture). The explicit permission-policy step closes that gap.
- Checks that need no network or secrets run on every push. Dependency review needs the pull request's dependency graph, so it runs only there.

## Consequences

- Pins are maintained by Dependabot and reviewed by a person. A pin that is bumped must match the SHA of the release named in its comment.
- `zizmor --offline` skips its online audits, including the check that a pinned SHA exists in the upstream action. Those audits need a token that the validation job does not hold.
- The permission-policy step is a line-based text check, not a YAML parser. It catches the usual forms (`contents: write`, `write-all`) but not every YAML construction. A structured check could replace it later.
- The pip install pins the version but not hashes (`--only-binary` only). Hash pinning is open.
- `go install` builds the three Go tools from source on each run. The Go module cache from `setup-go` speeds this up, but the first run is slow.
- The frontend job is a placeholder until F7. When the UI is embedded with `go:embed`, the Go jobs must depend on the frontend build. F7 owns that wiring. Node's version comes from `web/package.json`; `corepack` must be available on that Node version.
- The dependency-review job needs the repository's dependency graph enabled. If it is disabled, the action fails with a 403 (its documentation says so).
- Required status checks are a repository setting that a repository owner must configure. This ADR does not configure them.
- Open: no release, container or artifact upload job exists here (F10). The validation workflow does not publish binaries.
