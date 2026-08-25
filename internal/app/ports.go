// Package app holds BillyCore's use cases: the thin layer that orders domain
// operations and talks to the outside world through ports.
//
// Ports are declared here, where they are consumed, and implemented in
// internal/adapter (ARCHITECTURE.md §4, D6). Nothing in this package knows that
// storage is SQLite or that a Source is Gmail.
package app

import (
	"context"
	"errors"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// ErrEvidenceNotFound is returned by EvidenceRepository.GetByID when no
// Evidence has the requested id. It is declared with the port rather than in an
// adapter, because "there is no such Evidence" is part of the contract every
// implementation has to honour — and the HTTP layer turns it into a 404.
var ErrEvidenceNotFound = errors.New("evidence: not found")

// EvidenceRepository stores and reads Evidence.
type EvidenceRepository interface {
	// Insert records Evidence and reports whether a row was created. A false
	// with a nil error means the artifact was already recorded — the normal
	// outcome of re-syncing a mailbox, not a failure (D10).
	Insert(ctx context.Context, e domain.Evidence, now time.Time) (bool, error)

	// GetByID returns the Evidence with the given id.
	GetByID(ctx context.Context, id string) (domain.Evidence, error)

	// ExistsByReference reports whether an artifact has already been recorded,
	// without reading it back. Insert is still the authority on idempotency —
	// this only lets a sync avoid downloading an artifact it already holds.
	ExistsByReference(ctx context.Context, sourceID, sourceReference string) (bool, error)
}

// Artifact is what a Source hands over: bytes, and the facts about them that
// the Source itself can vouch for.
//
// It is not Evidence. Evidence is what BillyCore decides to record, and it is
// built by a domain constructor that enforces invariants an adapter cannot be
// trusted to have applied.
type Artifact struct {
	// Reference identifies the artifact within its Source, stably. For Gmail it
	// is the message id (D10, D23).
	Reference string

	// ContentType describes Content — `message/rfc822` for a whole email (D24).
	ContentType string

	// ObservedAt is when the Source received the artifact, according to the
	// Source itself. Never a timestamp the artifact claims about itself:
	// Evidence is hostile input (SECURITY.md §7, §8).
	ObservedAt time.Time

	// Content is the artifact verbatim. Nothing has parsed it, and nothing in
	// this layer will.
	Content []byte
}

// SourceFetcher reads artifacts from one configured Source.
//
// Implementations carry their own configuration — which mailbox, which query —
// so this interface never grows a parameter describing where to look.
type SourceFetcher interface {
	// ListReferences returns the reference of every artifact the Source
	// currently offers, oldest handling order unspecified.
	ListReferences(ctx context.Context) ([]string, error)

	// Fetch returns one artifact verbatim.
	Fetch(ctx context.Context, reference string) (Artifact, error)
}
