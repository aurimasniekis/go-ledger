# go-ledger

Ledger is a personal finance application for one technically comfortable person running it on their own machine. It is written in Go, with SQLite for storage and a React and TypeScript web UI embedded in the same binary.

Ledger's main view is a monthly picture of recurring commitments (services, servers, domains, subscriptions and debt repayments). For each one it shows what is paid, upcoming, overdue and still outstanding, next to the bank balances.

## What the balance comparison means

Ledger shows an **outlook against recorded commitments**. It compares bank balances with the commitments that are still due and that Ledger knows about.

It is **never** a guarantee of money that is safe to spend. Balances can be stale. Transactions can be pending or missing. A commitment that was never recorded is not counted. Amounts are combined only across one currency, or through an explicit conversion policy. Every figure carries its currency and the time it was last refreshed.

## Status

The repository is at the skeleton stage. It has the module, the package layout, the licence and the developer conventions. Features are being built in vertical slices; see [docs/plan.md](docs/plan.md).

## Development

```
make help     # list the targets
make check    # fmt-check, vet, lint, test (race), build
make build    # build bin/ledger
```

- [CONTRIBUTING.md](CONTRIBUTING.md): workflow and Conventional Commits.
- [docs/decisions/](docs/decisions/README.md): architecture decision records.

## Licence

Apache License 2.0. See [LICENSE](LICENSE).
