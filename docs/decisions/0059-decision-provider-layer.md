# ADR-0059: Decision-provider layer and policy enforcement

Status: Accepted (2026-11-22)

## Context

The requirements ask for model-assisted decisions (transaction categorisation,
label suggestions, alerts) through OpenRouter, OpenAI, Anthropic and TypeSafe,
each integrated directly, with provider and model chosen per task, outputs
validated and kept apart from bank facts, and consent that is specific to a
provider and a purpose. Decision D13 settled the policy: per-task {provider,
model, timeout, budget}; review by default, opt-in auto-apply for
categorisation only, always reversible; permitted fields description, amount,
currency, booking date and merchant name, never IBANs or account numbers,
counterparty names or balances; OpenRouter downstream sharing disabled unless
the operator names the upstream providers; a per-task monthly request cap and
at most two retries; no automatic fallback, an optional explicit per-task
list; model output relates to matching as suggestions only. Control defaults
extend it: each task declares a capability (decide or generate) and a provider
declares the ones it serves; model calls retry only failures returned before
generation and count every attempt; TypeSafe receives only the description,
the merchant name and candidate names.

This task (L1) builds the shared layer. The adapters are L2 to L5, the tasks
that run it are L6, the review UI and settings L7, the TypeSafe matcher X3.

## Decision

1. A domain package, `internal/domain/decisions`, owns the policy. It has a
   registry of tasks: `categorisation` (decide, one choice, auto-apply
   allowed), `label_suggestion` (decide, up to five choices),
   `alerts` (generate, up to 280 characters) and `payment_matching` (decide,
   one choice, review only) for the optional matcher. A task name is also the
   purpose consent is given for.
2. Providers are adapters behind the `Provider` port, each with a
   `Descriptor` (name, capabilities, whether it routes to other providers).
   `KnownProviders` lists the four from the research: TypeSafe is decide-only;
   OpenRouter routes. Configuration naming a provider that cannot serve the
   task's capability is refused, both against the known descriptors (at
   configuration load) and against the adapter's own descriptor (when the
   service is built).
3. Configuration is `decisions.tasks.<task>.{enabled, provider, model,
   capability, timeout, monthly_requests, retries, auto_apply, fields,
   fallback}` and `decisions.providers.<provider>.{api_key, api_key_file}`,
   plus `decisions.providers.openrouter.allowed_upstreams`. Keys are secrets:
   environment or file only, never the YAML file or a flag, registered with the
   log redactor. Every task is off by default.
4. Before anything reaches an adapter, the service builds the request from an
   allowlist for the provider and purpose: the task's configured `fields` for
   its main provider, otherwise the provider's default (the D13 list; for
   TypeSafe description, merchant name and candidate names). The excluded
   fields can never be allowed: configuration naming them is refused, and the
   request builder only copies permitted fields. Candidates go out as opaque
   keys (`c1`, `c2`, ...) with their names; Ledger's identifiers never leave
   the process, and the output schema is static per task with no user data.
   Values are cut at 500 characters.
5. Consent is exact: a provider may be called for a purpose only while an
   active consent for that pair is recorded (`decision_consents`). Consent for
   TypeSafe payment matching enables no other provider, purpose or upstream.
   Every route, fallback included, needs its own.
6. Each attempt is first counted against the task's monthly cap
   (`decision_usage`, calendar month in UTC); a full cap stops before sending.
   The task's timeout bounds one run on one route, retries included. Only an
   adapter's `retryable` error (a failure before generation) is retried, at
   most `retries` (0 to 2) times, after the provider's Retry-After or an
   exponential backoff with jitter, and never past the timeout.
7. The answer is validated strictly: exactly the schema's members, a
   confidence written as a plain number from 0 to 1 (kept as text, never a
   float), choices among the keys offered, no repeats, bounded text without
   control characters. An invalid answer is rejected: it is not stored, not
   retried and not sent to a fallback.
8. A valid answer becomes a pending suggestion in `decision_suggestions`,
   with the model the provider reported and its response id; a newer one for
   the same task and object supersedes it. Suggestions are never bank facts,
   payments, matches or assignments; triggers keep everything but the state
   unchangeable. Applying one is L6's job, through the owning feature's rules
   (organisation's model origin, which a person's choice outranks).
9. Any failure (no consent, cap reached, outage, timeout, rejection, invalid
   output) is a result with a reason and no suggestion, and changes nothing
   but the attempt count. Only an outage moves on to the next route of an
   explicit fallback list.
10. `ledger serve` builds the service when a task is enabled. This version has
    no adapters, so an enabled task stops startup with a clear error rather
    than doing nothing silently.

## Why

- Keeping the allowlist, consent, cap and validation in one place means an
  adapter cannot forget them, and each adapter only maps a filtered request to
  its API. Tests prove the policy once, with a recording test double, instead
  of per provider.
- Refusing invalid output instead of repairing it keeps a model's mistakes
  from turning into data; a person or a rule decides anyway.
- Opaque candidate keys keep identifiers private and let schemas stay static
  (schemas may be cached by providers).
- Counting attempts before sending makes the cap a hard limit even under
  concurrency (one conditional upsert).
- Fallback routes use their own provider's default allowlist so a wider
  allowlist chosen for one provider never widens what another receives.
- Rejected: a generic JSON Schema library (validation needs the offered
  candidates anyway, and the shapes are small); applying suggestions here
  (it belongs to the feature that owns the data); a reporting-zone month for
  the cap (UTC is simpler and the cap is a cost guard, not a report).

## Consequences

- Adapters (L2 to L5) implement `Provider`, classify errors as
  `retryable`, `unavailable` or `rejected`, and must send nothing beyond the
  request. OpenRouter must restrict routing to `Request.Upstreams`.
- Still open, and not decided here: how consent is granted and shown (the
  store has `Grant`/`Revoke`, audited as `decision_consent.granted`/
  `revoked`, but no API, CLI or UI yet: L7/X3), the consent texts and
  attestations the research asks for, the default monthly cap (500 is a
  proposal), applying and reviewing suggestions (L6, L7), suggestion retention,
  and whether free-text fields such as the description should be scrubbed of
  account numbers before sending (today the description is sent as the bank
  wrote it when the allowlist permits it).
- New migration `0031_decision_providers`; `decision_usage` is exempt from
  the version column (server bookkeeping).
