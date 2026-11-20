# ADR-0018: Release workflow and container image

Status: Accepted (2026-11-20). Amends the write-scope rule of [ADR-0007](0007-ci-validation-workflow.md) (rule 5) for one
release workflow.

## Context

F10 delivers a tag-triggered release that produces the binaries (linux amd64 and arm64, darwin arm64) with the web UI
embedded, checksums, SBOMs and the container image (D19). The operator chose Docker Hub for the image, `docker.io/aurimasniekis/ledger`,
with a repository secret holding a push token (the login username is fixed in the workflow). A GitHub Release needs
`contents: write`. ADR-0007 keeps every write scope out of `ci.yml`, and `scripts/check-workflow-permissions.sh` rejects a
write scope in every workflow file. The release therefore needs an explicit, narrow exception. The Terraform provider's
GPG signing belongs to `terraform-provider-ledger`, not to this repository.

## Decision

1. **Tools.** GoReleaser v2.20.3 (`.goreleaser.yaml`) builds the archives, checksums and images, and creates the release.
   syft v1.57.2 produces the SBOMs. Both are pinned in `release.yml` and installed with `go install`, as ADR-0007 rule 4
   requires. `make release-dry-run` runs the same pinned GoReleaser locally.
2. **Build.** `before` runs `make web-install` and `make web-build` first (ADR-0008). Each binary is built with
   `CGO_ENABLED=0` and `-trimpath`, and `main.version` is set from the tag. Targets: linux amd64 and arm64, darwin arm64.
3. **Artefacts.** `tar.gz` archives (with LICENSE and README), `checksums.txt` (SHA-256), and an SBOM for each archive and
   each binary.
4. **Image.** `docker.io/aurimasniekis/ledger` for linux/amd64 and linux/arm64, built from `Dockerfile`. The Dockerfile only
   copies the binary that GoReleaser built for each platform, so no emulation is needed. The base is
   `gcr.io/distroless/static-debian12:nonroot`, pinned by its index digest, and the process runs as uid 65532.
   Image defaults: `LEDGER_HTTP_LISTEN=:8080` (ADR-0010 and D17 require an explicit non-loopback listener in containers)
   and `LEDGER_DATABASE_PATH=/data/ledger.db`, where `/data` is writable by the non-root user. The reporting timezone is
   not defaulted: the first start needs `LEDGER_REPORTING_TIMEZONE` (an IANA name), as ADR-0010 requires.
   Tags: `<version>`, and `latest` for stable releases only.
5. **Workflow `.github/workflows/release.yml`.** Triggers: a tag push `v*` and `workflow_dispatch`. Top-level permissions
   are `contents: read`.
   - `build` (no secrets, no write scope): on a tag, a tagged build; on a dispatch, a snapshot. Both use
     `--skip=publish`, so nothing is pushed or released. It uploads `dist/` as a workflow artefact.
   - `registry-access` (tag pushes): records only whether the token secret is set. The value is never printed.
   - `publish-images` (tag pushes, secret set): logs in to Docker Hub with the token on stdin and runs
     `goreleaser release --skip=announce` with `LEDGER_DISABLE_GITHUB_RELEASE=true`, which turns off the GitHub Release
     (`release.disable` in `.goreleaser.yaml`). `--skip=publish` cannot be used here: it also skips the image push, and
     `--skip=release` is not a valid option.
   - `github-release` (tag pushes): `contents: write`, the only write scope in the repository. It runs
     `goreleaser release --skip=docker,announce`.
6. **Policy amendment.** A write scope is allowed only in `release.yml`, only as `contents: write` under a job-level
   `permissions:` block, and only in a job whose `if:` names both `github.event_name == 'push'` and `refs/tags/`. Any other
   write scope, in any file or at top level, fails `make workflow-policy`. The self-test has fixtures for each case.
7. **Human gate.** The GitHub Release is a draft (`release.draft: true`). A person reviews the artefacts and publishes it.
8. **Actions.** `actions/upload-artifact` (an official GitHub action, pinned to v7.0.2) is added to the official set that
   ADR-0007 lists. Caches are off in the release jobs (`setup-go cache: false`, `package-manager-cache: false`), so a cache
   written by one run cannot reach a published artefact.

## Why

- **GoReleaser** produces the archives, checksums, SBOMs, images and the release from one configuration, and its image
  builder can build arm64 from an amd64 runner with a COPY-only Dockerfile (verified). Hand-written `go build` and
  `docker` steps would duplicate that logic.
- **Docker Hub with the `docker` CLI** follows D19. A `docker/*` action would add third-party actions that ADR-0007
  keeps out.
- **A narrow policy exception**, not a relaxed policy. The alternatives were a write scope at top level (rejected), a
  release job outside `.github/workflows` (it would bypass the check), and no GitHub Release (the binaries would exist
  only as run artefacts).
- **Draft by default**: nothing becomes public until a person publishes it.

## Consequences

- Verified locally: `goreleaser check`; a snapshot release (three binaries, three archives, checksums, six SBOMs, two
  platform images); the amd64 image starts as non-root, serves the UI (`/`, a deep link) and returns the JSON 404 for
  `/api/...`; a tag run with the release disabled pushed the images to a throwaway local registry (an rc tag gets no
  `latest`, a stable tag gets `latest`, and both platforms are in the manifest); the permission policy self-test and the
  policy on `.github/workflows`; actionlint 1.7.12; zizmor 1.32.0 (offline, low severity and up); `make check`.
- **Not verified:** a GitHub Actions run of this workflow, the Docker Hub push, the GitHub Release creation (it needs a
  token and a tag, and no tag is pushed to the upstream repository, because a tag there would publish with the real
  secret), and the arm64 image at run time (no emulator on the build host).
- A tag build with `--skip=publish` builds no images (GoReleaser builds and pushes them in one step). A tag dry run
  therefore produces the binaries, archives, checksums and SBOMs; the images are built by the dispatch snapshot and pushed
  only by `publish-images`.
- **Open:**
  - ADR-0003 requires third-party licence texts to ship with binaries and images. The archives and the image contain only
    LICENSE and README until OP4 produces the licence report.
  - The base image is pinned by digest, and nothing updates that digest automatically yet.
  - SLSA provenance and attestations would need `id-token: write` and `attestations: write`. They are not included and would
    need another policy amendment.
  - The management listener is loopback-only, so inside the container its health and metrics endpoints are not reachable
    from the host through a port mapping. This belongs to OP1.
  - Publishing the Terraform provider stays in its own repository (GPG signing, then the Registry step).
