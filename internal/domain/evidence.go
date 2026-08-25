package domain

import (
	"errors"
	"fmt"
	"time"
)

// SourceType is the kind of Source an artifact came from, not the specific
// account. Two Gmail accounts are two Sources sharing one SourceType.
type SourceType string

const (
	SourceGmail         SourceType = "GMAIL"
	SourceBankStatement SourceType = "BANK_STATEMENT"
	SourceManual        SourceType = "MANUAL"
)

func (t SourceType) Validate() error {
	switch t {
	case SourceGmail, SourceBankStatement, SourceManual:
		return nil
	default:
		return fmt.Errorf("source type: %q is not a known kind of Source", string(t))
	}
}

var (
	ErrEvidenceNoID              = errors.New("evidence: id is required")
	ErrEvidenceNoSourceID        = errors.New("evidence: source id is required")
	ErrEvidenceNoSourceReference = errors.New("evidence: source reference is required")
	ErrEvidenceNoObservedAt      = errors.New("evidence: observed_at is required")
)

// Evidence is something Billy actually observed from a Source.
//
// **Immutable once recorded** (DOMAIN.md §4). That invariant is enforced by this
// type, not by convention: every field is unexported, there are no setters, and
// raw content is copied on the way in and on the way out so no caller retains a
// handle that could mutate a recorded artifact.
//
// Evidence has no domain state. `processing_stage`, `attempts`, `last_error`,
// and `locked_until` are pipeline columns and are deliberately absent here —
// they are infrastructure, and DOMAIN.md §5 keeps them out of the domain
// entirely (DATA_MODEL.md §7).
//
// Evidence is not interpretation. Nothing here knows what an email means; that
// is a Claim, and it is derived from Evidence rather than stored inside it.
type Evidence struct {
	id              string
	sourceID        string
	sourceType      SourceType
	sourceReference string
	contentType     string
	rawContent      []byte
	observedAt      time.Time
}

// NewEvidence records an observed artifact, enforcing every invariant Evidence
// carries. This is the one place the question "is this valid Evidence?" is
// answered, whether the artifact arrived from a Source adapter or over HTTP
// (ARCHITECTURE.md §4, D6).
//
// The id is supplied rather than generated: generating a UUID reads from the
// operating system, and the domain performs no I/O (D6).
//
// sourceReference is Evidence identity together with sourceID (D10, D23). For
// Gmail it is the message id.
//
// rawContent may be nil. DATA_MODEL.md §4.1 permits it when sourceReference is
// itself the preserved reference to the artifact.
func NewEvidence(
	id, sourceID string,
	sourceType SourceType,
	sourceReference, contentType string,
	rawContent []byte,
	observedAt time.Time,
) (Evidence, error) {
	if id == "" {
		return Evidence{}, ErrEvidenceNoID
	}
	if sourceID == "" {
		return Evidence{}, ErrEvidenceNoSourceID
	}
	if err := sourceType.Validate(); err != nil {
		return Evidence{}, err
	}
	if sourceReference == "" {
		return Evidence{}, ErrEvidenceNoSourceReference
	}
	if observedAt.IsZero() {
		return Evidence{}, ErrEvidenceNoObservedAt
	}
	return Evidence{
		id:              id,
		sourceID:        sourceID,
		sourceType:      sourceType,
		sourceReference: sourceReference,
		contentType:     contentType,
		rawContent:      copyBytes(rawContent),
		observedAt:      observedAt.UTC(),
	}, nil
}

func (e Evidence) ID() string              { return e.id }
func (e Evidence) SourceID() string        { return e.sourceID }
func (e Evidence) SourceType() SourceType  { return e.sourceType }
func (e Evidence) SourceReference() string { return e.sourceReference }
func (e Evidence) ContentType() string     { return e.contentType }
func (e Evidence) ObservedAt() time.Time   { return e.observedAt }

// RawContent returns a copy. Handing out the slice itself would let a caller
// rewrite a recorded artifact through the returned reference, which is exactly
// what the immutability invariant forbids.
func (e Evidence) RawContent() []byte { return copyBytes(e.rawContent) }

// ContentBytes is the artifact's size. API.md §4 puts it on the wire; nothing
// stores it, because it is derivable (D24).
func (e Evidence) ContentBytes() int { return len(e.rawContent) }

// HasRawContent distinguishes "Billy stored the artifact" from "Billy kept only
// a reference to it" — different situations, and not the same as empty content.
func (e Evidence) HasRawContent() bool { return e.rawContent != nil }

func copyBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}
