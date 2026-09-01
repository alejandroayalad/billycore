package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// ErrEvidenceNotFound is app.ErrEvidenceNotFound: the port defines what "no
// such Evidence" means, and this adapter reports it rather than inventing a
// second sentinel that callers would have to know about.
var ErrEvidenceNotFound = app.ErrEvidenceNotFound

// timeLayout is the timestamp format DATA_MODEL.md §2 fixes: UTC RFC 3339 with
// milliseconds, so text ordering matches chronological ordering.
const timeLayout = "2006-01-02T15:04:05.000Z07:00"

// stageReceived is where ingestion leaves Evidence. ARCHITECTURE.md §5:
// ingestion records the artifact and stops; extraction is a later stage that
// claims rows from the database (D7).
const stageReceived = "RECEIVED"

// eventEvidenceIngested is the event name from DOMAIN.md §8.
const eventEvidenceIngested = "EvidenceIngested"

// EvidenceRepository stores and reads Evidence.
//
// It returns domain types directly. There is no persistence model and no mapper
// layer (DATA_MODEL.md, preamble).
type EvidenceRepository struct {
	db *sql.DB
}

func NewEvidenceRepository(db *sql.DB) *EvidenceRepository {
	return &EvidenceRepository{db: db}
}

// Insert records Evidence and reports whether a row was created.
//
// Ingestion is an upsert on `(source_id, source_reference)` (D10). The same
// email fetched twice produces one row, and the second call reports
// created=false rather than failing — re-syncing a mailbox is the normal case,
// not an error.
//
// The `EvidenceIngested` event is written here, in the same transaction, and
// only when a row was actually created (D25). Two reasons it cannot live in the
// use case: an event describing a change that did not commit is a lie about the
// domain (DATA_MODEL.md §4.7), and a skipped duplicate is not an ingestion —
// emitting an event for it would make every re-sync look like 1,044 new
// artifacts to every consumer.
//
// `now` is passed in rather than read from the clock so the caller owns time.
func (r *EvidenceRepository) Insert(ctx context.Context, e domain.Evidence, profile app.ExtractionProfile, now time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("insert evidence: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	res, err := tx.ExecContext(ctx, `
		INSERT INTO evidence (
			id, source_id, source_type, source_reference,
			content_type, raw_content, observed_at, created_at,
			processing_stage, extraction_profile
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, source_reference) DO NOTHING`,
		e.ID(), e.SourceID(), string(e.SourceType()), e.SourceReference(),
		nullableText(e.ContentType()), e.RawContent(),
		formatTime(e.ObservedAt()), formatTime(now),
		stageReceived, nullableText(string(profile)),
	)
	if err != nil {
		// Never include the row values: raw_content is in them (SECURITY.md §10).
		return false, fmt.Errorf("insert evidence: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("insert evidence: rows affected: %w", err)
	}
	if affected == 0 {
		// Already recorded. Evidence is immutable — a re-fetch never overwrites
		// what was stored the first time (D7, D10).
		return false, tx.Commit()
	}

	if err := appendEvidenceIngested(ctx, tx, e, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("insert evidence: commit: %w", err)
	}
	return true, nil
}

// GetByID reads one Evidence back through its domain constructor.
//
// Reconstructing rather than filling the struct means storage cannot produce
// Evidence that the domain would have rejected — a row corrupted or written by
// an older binary surfaces as an error here instead of as an invalid domain
// object flowing onward (D6, D11).
func (r *EvidenceRepository) GetByID(ctx context.Context, id string) (domain.Evidence, error) {
	return scanEvidence(r.db.QueryRowContext(ctx, `
		SELECT id, source_id, source_type, source_reference, content_type, raw_content, observed_at
		FROM evidence
		WHERE id = ?`, id))
}

// GetByReference returns the row that won the Source identity slot. It is the
// original Evidence after an idempotent direct-upload retry (D53).
func (r *EvidenceRepository) GetByReference(ctx context.Context, sourceID, sourceReference string) (domain.Evidence, error) {
	return scanEvidence(r.db.QueryRowContext(ctx, `
		SELECT id, source_id, source_type, source_reference, content_type, raw_content, observed_at
		FROM evidence
		WHERE source_id = ? AND source_reference = ?`, sourceID, sourceReference))
}

func scanEvidence(row *sql.Row) (domain.Evidence, error) {
	var (
		id, sourceID, sourceType, sourceReference string
		contentType                               sql.NullString
		rawContent                                []byte
		observedAt                                string
	)
	err := row.Scan(&id, &sourceID, &sourceType, &sourceReference, &contentType, &rawContent, &observedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Evidence{}, ErrEvidenceNotFound
	}
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("get evidence: %w", err)
	}

	observed, err := time.Parse(time.RFC3339, observedAt)
	if err != nil {
		return domain.Evidence{}, fmt.Errorf("get evidence: observed_at is not RFC 3339: %w", err)
	}
	return domain.NewEvidence(
		id, sourceID, domain.SourceType(sourceType),
		sourceReference, contentType.String, rawContent, observed,
	)
}

// appendEvidenceIngested writes the event described in DOMAIN.md §8: what Billy
// recorded, which artifact it came from, and when it was observed.
//
// The payload carries no content. It is a fact about an artifact, not a copy of
// one (SECURITY.md §10).
func appendEvidenceIngested(ctx context.Context, tx *sql.Tx, e domain.Evidence, now time.Time) error {
	payload, err := json.Marshal(struct {
		EvidenceID      string `json:"evidenceId"`
		SourceReference string `json:"sourceReference"`
		ObservedAt      string `json:"observedAt"`
	}{
		EvidenceID:      e.ID(),
		SourceReference: e.SourceReference(),
		ObservedAt:      formatTime(e.ObservedAt()),
	})
	if err != nil {
		return fmt.Errorf("%s: marshal payload: %w", eventEvidenceIngested, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO domain_event (type, payload, occurred_at) VALUES (?, ?, ?)`,
		eventEvidenceIngested, string(payload), formatTime(now),
	); err != nil {
		return fmt.Errorf("%s: %w", eventEvidenceIngested, err)
	}
	return nil
}

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// nullableText keeps missing information as NULL. DATA_MODEL.md §2 forbids
// standing in for it with "" — absent and empty are different facts.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ExistsByReference reports whether an artifact is already recorded.
//
// It reads no content: the answer is one index probe on
// `UNIQUE (source_id, source_reference)`, which is why DATA_MODEL.md §5 declares
// no second index for this lookup.
func (r *EvidenceRepository) ExistsByReference(ctx context.Context, sourceID, sourceReference string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM evidence WHERE source_id = ? AND source_reference = ?`,
		sourceID, sourceReference,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("look up evidence by source reference: %w", err)
	}
	return true, nil
}
