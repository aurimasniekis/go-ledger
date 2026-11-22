// Package organisation models how a person organises their finances (task O1,
// ADR-0053, decision D12): categories, labels and buckets, and the assignment
// of them to objects.
//
//   - A category is single-valued per object and lives in a two-level
//     hierarchy (a parent and its children, no deeper).
//   - A label is many-valued and flat.
//   - A bucket groups commitments and accounts to slice the outlook. It never
//     holds, allocates or reserves money and is not a budget envelope.
//
// Categories and labels apply to transactions, commitments, accounts and
// merchants; buckets to commitments and accounts only.
//
// Definitions are archived before they are deleted: an archived definition
// keeps its historical assignments and nothing new may use it; a definition
// that is still assigned is deleted only with an explicit reassignment.
//
// Every assignment records its [Origin]: the person (user), a rule, or a
// model. The person's word always stands: an automatic origin never replaces a
// user assignment, nor brings back a category the person cleared or a label
// the person removed ([CanReplace]). Assignments live apart from bank facts,
// so a bank refresh never changes them. Nothing here refers to amounts or
// payment state.
//
// The package is pure: persistence is the [Store] interface.
package organisation
