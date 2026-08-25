package app

import (
	"context"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
	"github.com/alejandroayalad/billycore/internal/id"
)

// Source is a configured Source, as far as ingestion is concerned: an identity
// and a kind. Where it is configured, and how the fetcher was built from that
// configuration, is not this layer's business (D26).
type Source struct {
	ID   string
	Type domain.SourceType
}

// SyncResult is the summary API.md §5 returns.
type SyncResult struct {
	SourceID            string
	ArtifactsDiscovered int
	EvidenceCreated     int
	EvidenceSkipped     int
	CompletedAt         time.Time
}

// Ingestor records what a Source offers, and stops.
//
// It creates no Claims and extracts no financial fields. ARCHITECTURE.md §5
// makes that a stage boundary rather than a to-do: Evidence is persisted before
// anything interprets it, so a parser that crashes cannot lose an artifact that
// may no longer be fetchable (D7).
type Ingestor struct {
	Repo EvidenceRepository

	// NewID and Now are fields rather than package calls so a test can run the
	// whole use case with no clock and no randomness — and so this file needs
	// no database and no network to be exercised.
	NewID func() (string, error)
	Now   func() time.Time
}

func NewIngestor(repo EvidenceRepository) *Ingestor {
	return &Ingestor{
		Repo:  repo,
		NewID: id.New,
		Now:   func() time.Time { return time.Now().UTC() },
	}
}

// Sync fetches from one Source and records whatever it yields.
//
// Synchronous by design (API.md §5): sync records Evidence, and extraction runs
// behind it on its own pass. There is no job to poll.
//
// A fetch that fails aborts the sync and returns the error rather than skipping
// the artifact. Skipping would report a count that quietly means something else,
// and the alternative is cheap precisely because ingestion is idempotent — a
// re-run re-lists everything, skips what is already stored, and resumes where
// the failure left off.
func (in *Ingestor) Sync(ctx context.Context, src Source, fetcher SourceFetcher) (SyncResult, error) {
	result := SyncResult{SourceID: src.ID}

	references, err := fetcher.ListReferences(ctx)
	if err != nil {
		return SyncResult{}, fmt.Errorf("sync %s: list artifacts: %w", src.ID, err)
	}
	result.ArtifactsDiscovered = len(references)

	for _, reference := range references {
		if err := ctx.Err(); err != nil {
			return SyncResult{}, err
		}
		created, err := in.ingestOne(ctx, src, fetcher, reference)
		if err != nil {
			return SyncResult{}, err
		}
		if created {
			result.EvidenceCreated++
		} else {
			result.EvidenceSkipped++
		}
	}

	result.CompletedAt = in.Now().UTC()
	return result, nil
}

// ingestOne records a single artifact, reporting whether it was new.
func (in *Ingestor) ingestOne(ctx context.Context, src Source, fetcher SourceFetcher, reference string) (bool, error) {
	// Ask before downloading. Evidence is immutable, so an artifact already
	// recorded has nothing to learn from a re-fetch — and on a mailbox of a
	// thousand messages, re-downloading every one of them to discover that is
	// the difference between a sync that takes seconds and one that does not.
	// Insert remains the authority: if the row appears between this check and
	// the write, ON CONFLICT still reports it as skipped (D10).
	exists, err := in.Repo.ExistsByReference(ctx, src.ID, reference)
	if err != nil {
		return false, fmt.Errorf("sync %s: %w", src.ID, err)
	}
	if exists {
		return false, nil
	}

	artifact, err := fetcher.Fetch(ctx, reference)
	if err != nil {
		// The reference is a message id, not content, so it is safe to name.
		return false, fmt.Errorf("sync %s: fetch artifact %s: %w", src.ID, reference, err)
	}

	evidenceID, err := in.NewID()
	if err != nil {
		return false, fmt.Errorf("sync %s: generate id: %w", src.ID, err)
	}
	evidence, err := domain.NewEvidence(
		evidenceID, src.ID, src.Type,
		artifact.Reference, artifact.ContentType, artifact.Content, artifact.ObservedAt,
	)
	if err != nil {
		// Never wrap the artifact itself into an error: it ends up in a log
		// (SECURITY.md §10).
		return false, fmt.Errorf("sync %s: artifact %s is not valid Evidence: %w", src.ID, reference, err)
	}

	created, err := in.Repo.Insert(ctx, evidence, in.Now())
	if err != nil {
		return false, fmt.Errorf("sync %s: record artifact %s: %w", src.ID, reference, err)
	}
	return created, nil
}
