# ADR-0003: Configuration system

Status: Accepted (2026-11-20)

## Context

Task F2 needs one typed configuration, filled from a YAML file, environment variables, a `.env` file and flags, with strict errors and no secrets in files. The Configuration and `.env` loading preset blocks (snapshot 2026-11-20) and decision D2 (prefix `LEDGER_`) apply.

## Decision

- **One struct is the definition.** `config.Config` in `internal/config` carries tags (`cfg`, `default`, `desc`, `secret`, `enum`) on each leaf. Reflection derives the key, the variable `LEDGER_<UPPER_PATH>`, the flag, the default and the documentation. A new field needs no other wiring. A test rejects missing descriptions, duplicate keys or variables and unsupported types.
- **Layers, lowest to highest:** defaults, YAML, environment (the real environment over the `.env` file), flags. `--config` beats `LEDGER_CONFIG`. A missing YAML file is allowed; `config check` says it was not found. Required values (today the key-encryption key) are validated after merging, so the environment can supply them.
- **Strictness.** The YAML is parsed to a node tree and walked against the definition, so unknown keys, duplicate keys, wrong types, a second document and enum or range violations are all collected and reported together with line numbers. A YAML integer is not accepted for a string, and a duration must be a string with a unit.
- **Empty means unset.** `LEDGER_X=` does not override a lower layer. This keeps `.env` templates with blank entries harmless. The cost: an empty string cannot be forced over a non-empty lower layer. No current key needs that.
- **Secrets.** A key marked `secret` is never accepted in YAML (the problem names the variable to use), has no flag (command lines are visible in process listings) and is never printed: `config check` shows `<set>` or `<unset>`, errors hide its value, and generated documentation shows defaults only. `kek.file` (also settable in YAML) names a file whose content, trailing newlines trimmed, becomes the value. Setting both is an error.
- **`.env` grammar.** `KEY=VALUE`, comments, optional `export`, single quotes (literal) and double quotes (only the escapes `\n`, `\r`, `\t`, `\"` and `\\`), no expansion, no multi-line values. The first malformed line stops loading with `file:line: message`; the message never contains the line's text. The real environment wins; `config check` lists by name the variables the file contributed. A `#` that starts an unquoted value, or follows whitespace, begins a comment (`A= # note` is empty). A missing file, including one named by `LEDGER_ENV_FILE`, is not an error, as the preset says. `LEDGER_ENV_FILE` is read from the real environment only. The loader does not call `os.Setenv`: the merged view is a lookup function, so tests need no process state.
- **Examples are generated.** `config.example.yaml` and `.env.example` are produced from the definition (`make config-examples`) and a test fails when they differ from the committed copies. A second check (`CheckExamples`) lists any variable or key with no entry and rejects a secret key in the YAML example. `ledger config env` prints the same text as `.env.example`, so it never contains a configured value.
- **Dependency.** `go.yaml.in/yaml/v3` (the maintained continuation of `gopkg.in/yaml.v3`; MIT and Apache-2.0), pure Go. `time/tzdata` is imported so `reporting.timezone` resolves on hosts without a zoneinfo database (about 450 KB).
- **Initial keys** are the ones early slices need: HTTP server limits and timeouts, database path and busy timeout, logging, reporting timezone, KEK. Later tasks add keys by adding fields and regenerating the examples.

## Consequences

- Operators get one precedence rule and all configuration mistakes in one run.
- The timeout and body-limit defaults are placeholders for a single-user local deployment. The unresolved policy decisions on timeouts and thresholds stay open; the generated reference shows the values and they can change before the first release.
- Reflection hides the key list from grep. The generated example files are the readable index.
- `kek` is required now although nothing consumes it until the credential store lands, so the "required values from the environment" path is exercised from the start.
