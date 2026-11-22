// Package decisions is the shared decision-provider layer for model-assisted
// suggestions (task L1, ADR-0057, decision D13 and the control defaults that
// extend it).
//
// It owns everything that must hold whichever provider answers:
//
//   - a registry of decision tasks (categorisation, label suggestions, alerts
//     and the optional payment-matching purpose), each with its capability
//     (decide: a closed-set choice; generate: free text) and output kind;
//   - per-task configuration {provider, model, capability, timeout, monthly
//     request cap, retries, review or auto-apply, fields, fallback};
//   - provider capability descriptors, so a task is never routed to a provider
//     that cannot serve its capability (no assumed interchangeability);
//   - consent per provider and purpose: consent for one pair never enables
//     another provider or another purpose;
//   - a per-provider-and-purpose field allowlist applied before anything is
//     handed to a provider: a field outside it, and the always-excluded fields
//     (IBANs and account numbers, counterparty names, balances), never reach a
//     provider adapter;
//   - strict validation of the model's output against the task's schema and
//     the candidates offered;
//   - budgets (a monthly request cap per task counting every attempt),
//     timeouts and retries (pre-generation failures only, at most two);
//   - suggestion storage apart from bank facts, matches and payments.
//
// A failure of any kind (no consent, budget spent, outage, timeout, invalid
// output) yields "no suggestion" and changes nothing: nothing here writes a
// payment, a match, a category or a label. Applying a suggestion is the
// caller's business (task L6), always reversible and only ever as a model
// origin, which a person's decision outranks.
//
// Concrete adapters (OpenRouter, OpenAI, Anthropic, TypeSafe) live under
// internal/providers and implement [Provider]; this package never imports
// them. Secrets (API keys) never pass through here: an adapter gets its key
// from configuration, which reads it from the environment or a file only.
package decisions
