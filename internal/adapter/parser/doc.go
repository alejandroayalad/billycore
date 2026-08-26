// Package parser turns stored Evidence into extracted field values.
//
// It reads Evidence and never writes it (D7, D10). Everything here is a pure
// function of its input: no clock, no network, no filesystem. A value that
// cannot be recognised is an error, never a guess — DOMAIN.md §7's rule that
// Billy may be incomplete but never unsupported starts at this layer.
//
// # Evidence is hostile input (D16, SECURITY.md §7)
//
// Nothing here renders, executes, or resolves anything. Text extraction is a
// byte scan that discards markup; it never fetches an <img>, never follows a
// link, and never expands an entity outside the fixed table in html.go. Nu's
// emails carry tracking pixels, so a parser that resolved remote references
// would report every re-parse to Nu's ESP.
//
// The scanner is iterative rather than recursive, so nesting depth is bounded
// by the input length rather than by the stack, and input size is capped at
// [MaxInputBytes].
//
// # ReDoS
//
// SECURITY.md §7 forbids regular expressions on unbounded input without a
// timeout. Go's regexp is RE2: it has no backtracking and runs in time linear
// in the length of the input, so no pattern in this package can backtrack
// catastrophically. That is a property of the language, and it is stated here
// once rather than re-argued at each pattern.
package parser
