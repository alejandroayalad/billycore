package app

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Failure classes. `last_error` is built from this closed vocabulary and never
// from err.Error(), because an error that travelled through a parser has the
// artifact inside it (SECURITY.md §10).
const (
	// classInterpret — a template recognised the artifact and then failed to
	// read it. This is the one that means "Nu changed something".
	classInterpret = "INTERPRET"

	// classPanic — a parser panicked and was recovered. SECURITY.md §7: one bad
	// email must not stop the pipeline.
	classPanic = "PANIC"

	// classClaim — the fields were read but did not make a valid Claim. The
	// domain rejected Billy's own interpretation, which is a bug in the mapping
	// rather than a bad artifact.
	classClaim = "CLAIM"

	// classStore — the Claim was valid and could not be written.
	classStore = "STORE"
)

// maxReasonLength bounds what reaches the column.
//
// `last_error` is a diagnostic, not a transcript. A cap means a pathological
// error string cannot grow a row without limit, and it is a second line of
// defence behind the redaction itself: whatever slips through is bounded.
const maxReasonLength = 200

// RetryBase and RetryCap bound the backoff between attempts.
//
// DATA_MODEL.md §7 specifies the mechanism — attempts, last_error, locked_until
// — and declines to name a schedule, the same way ARCHITECTURE.md §7 declined to
// name a busy timeout. These are the values chosen for it.
//
// Doubling from a minute, capped at a day. The cap is the important half and it
// is chosen against the failure that actually happens here: Nu changes a
// template, a few hundred artifacts start failing, and the fix is a new parser.
// A capped backoff means those rows retry themselves within a day of the fix
// shipping, with nobody resetting anything. That is why there is deliberately
// **no maximum attempt count** and no dead-letter stage — a row parked forever
// after five tries would need a hand to bring it back, and DATA_MODEL.md §7 has
// already ruled out expressing "failed" as a stage.
const (
	RetryBase = time.Minute
	RetryCap  = 24 * time.Hour
)

// retryAfter is how long a row waits before the next attempt.
//
// Exponential in the attempt count, so a genuinely poisonous artifact costs one
// parse a day rather than one per pass, while a transient failure is retried
// almost immediately.
func retryAfter(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	backoff := RetryBase
	for i := 1; i < attempts; i++ {
		backoff *= 2
		if backoff >= RetryCap {
			return RetryCap
		}
	}
	return backoff
}

// reason renders a failure for last_error: a class, and a detail drawn from
// something that promised to be safe.
//
// The err is never rendered. If it can describe itself without its input it is
// asked to; otherwise the class stands alone. There is no path here that calls
// err.Error(), and that is the point — a redaction that has to remember to be
// applied is one that will eventually be forgotten.
func reason(class string, err error) string {
	var safe Redacted
	if errors.As(err, &safe) {
		if detail := sanitise(safe.Redacted()); detail != "" {
			return truncate(class + ": " + detail)
		}
	}
	return class
}

// sanitise strips anything that would make a stored diagnostic unreadable or
// forgeable in a log: control characters, newlines, and runs of whitespace.
//
// A redacted reason is written by our own code, so this guards against a
// mistake rather than an attack — but the artifact is hostile input and the
// classification travelled from a layer that touched it, so it is checked at
// the boundary anyway.
func sanitise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t' || unicode.IsSpace(r):
			space = true
		case unicode.IsControl(r) || !unicode.IsPrint(r):
			// dropped entirely
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

func truncate(s string) string {
	if len(s) <= maxReasonLength {
		return s
	}
	// Cut on a rune boundary so the column never holds half a character.
	cut := maxReasonLength - 1
	for cut > 0 && !isRuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// panicked is the error a recovered panic becomes.
//
// It carries the panic value's *type* and never the value. A panic value is
// frequently a string, and a string inside a parser is frequently a piece of
// the artifact — `panic("unexpected token " + line)` is the shape that would
// write an email into last_error and into every log that reads it
// (SECURITY.md §7, §10).
type panicked struct {
	valueType string
}

func (p panicked) Error() string { return "recovered panic from " + p.valueType }

// Redacted satisfies the interface with something already safe: a Go type name
// is code, not content.
func (p panicked) Redacted() string { return "panic in " + p.valueType }

func newPanicked(v any) panicked {
	return panicked{valueType: fmt.Sprintf("%T", v)}
}
