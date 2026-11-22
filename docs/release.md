# Releasing Ledger

Design and tradeoffs: [ADR-0018](decisions/0018-release-workflow.md). This page is the operator's checklist.

## What a release produces

- Binaries for linux amd64, linux arm64 and darwin arm64 (pure Go, web UI embedded), as `.tar.gz` archives.
- `checksums.txt` (SHA-256) and SBOMs (syft) for every archive and binary.
- Multi-arch container images (linux/amd64, linux/arm64) at `docker.io/aurimasniekis/ledger`, tagged `<version>` and, for
  stable releases only (no `-rc` or other pre-release suffix), `latest`. The image runs as a non-root user.
- A **draft** GitHub Release that a person reviews and publishes.

## Steps

1. Make sure `work`/`main` is green (`make check`) and the repository secret `DOCKERHUB_TOKEN` (a Docker Hub push token)
   is set. The login username is the literal `aurimasniekis`; no username secret exists.
2. Optionally rehearse: run the `release` workflow manually (`workflow_dispatch`). It builds everything as a snapshot and
   uploads `dist/` as an artefact; it publishes nothing.
3. Tag and push: `git tag v1.2.3 && git push <remote> v1.2.3`. The `publish-images` job pushes the images and the
   `github-release` job creates the draft release. There is no approval gate, so anyone who can push a `v*` tag starts publishing.
4. Review the draft release (archives, checksums, SBOMs) and publish it.

## Local checks (offline)

- `make release-check`: validates `.goreleaser.yaml` with the pinned GoReleaser version.
- `make release-dry-run`: snapshot build without publishing (needs `syft` on PATH and, for images, a Docker daemon).
- `make workflow-policy`: the workflow permission policy (part of `make check`).

## Not covered here

The Terraform provider is released from its own repository with its own workflow and signing key.
