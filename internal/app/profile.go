package app

import "errors"

// ExtractionProfile names one reading contract: the format a Source gives, and
// the parser that reads it. `NU_EMAIL_V1` and `NU_STATEMENT_V1` are two of
// them. The vocabulary is closed and an adapter holds it, because a profile
// names a parser and the domain has no parsers (D6).
type ExtractionProfile string

// ErrNoProfile reports Evidence that names no profile. BillyCore has no default
// profile: a guess reads a statement with an email parser.
var ErrNoProfile = errors.New("dispatch: this evidence names no extraction profile")

// ErrUnknownProfile reports a profile that BillyCore does not support.
var ErrUnknownProfile = errors.New("dispatch: this extraction profile is not supported")

// ErrNoParserYet reports a supported profile that has no parser yet. The
// profile is valid in configuration, and BillyCore cannot read its artifacts.
var ErrNoParserYet = errors.New("dispatch: this extraction profile has no parser yet")

// ErrSignatureMismatch reports an artifact that is not the format the profile
// expects. It is a failure, and not an ordinary result: the Source states what
// its artifacts are, and this one is different. ErrNoInterpretation is the
// other case — the format is correct and the artifact carries no event.
var ErrSignatureMismatch = errors.New("dispatch: this artifact is not the format the profile expects")

// InterpreterRegistry selects the parser of one artifact.
type InterpreterRegistry interface {
	// Select returns the Interpreter of the profile. First it validates the
	// format signature: the content type, and the first bytes where the format
	// has a signature. It reads no more of the artifact than the signature.
	Select(profile ExtractionProfile, contentType string, raw []byte) (Interpreter, error)
}

// dispatchFailure describes a dispatch error without the artifact. It names the
// profile, which is configuration and not content (SECURITY.md §10).
type dispatchFailure struct {
	profile ExtractionProfile
	err     error
}

func (d dispatchFailure) Error() string { return d.Redacted() }
func (d dispatchFailure) Unwrap() error { return d.err }

// Redacted renders the sentinel and the profile name. Every sentinel above is
// fixed text, so no part of the artifact travels with it.
func (d dispatchFailure) Redacted() string {
	if d.profile == "" {
		return d.err.Error()
	}
	return d.err.Error() + ": " + string(d.profile)
}
