// Package domain is the parent of the business-rule packages. Each concept
// (money, commitment, occurrence, match, outlook, ...) is a sibling package at
// internal/domain/<name>.
//
// Domain packages import neither HTTP, SQL nor provider packages. They declare
// the interfaces they need (ports); store and providers implement them. The
// rule is enforced by depguard in .golangci.yml. See ADR-0001.
package domain
