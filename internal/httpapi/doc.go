// Package httpapi handles HTTP only: routing, request decoding and limits,
// structured error mapping, authentication middleware and response encoding.
//
// It calls domain packages directly and contains no business rules and no SQL.
// See ADR-0001.
package httpapi
