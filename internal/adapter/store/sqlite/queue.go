package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

// EvidenceQueue moves Evidence rows through the pipeline.
//
// It writes the four infrastructure columns DATA_MODEL.md §7 names —
// processing_stage, attempts, last_error, locked_until — and it is a separate
// type from EvidenceRepository for the reason §7 gives: those columns are not
// part of the Evidence domain object, and a repository that returns Evidence
// must not hand them out as if they were. The artifact itself stays immutable;
// only its position in the pipeline moves.
type EvidenceQueue struct {
	db *sql.DB
}

func NewEvidenceQueue(db *sql.DB) *EvidenceQueue {
	return &EvidenceQueue{db: db}
}

// ClaimForExtraction locks up to limit rows at stage RECEIVED and returns them.
//
// The claim and the read are one transaction, so two passes cannot take the
// same row: the UPDATE is what reserves it, and a second pass arriving mid-flight
// finds locked_until in the future and skips it.
//
// A lock that has lapsed is claimable again. That is what makes a killed
// process recoverable without anyone releasing anything by hand — the row was
// never modified, only reserved, and Evidence is immutable so there is nothing
// half-written to repair.
func (q *EvidenceQueue) ClaimForExtraction(ctx context.Context, limit int, now, lockedUntil time.Time) ([]app.PendingEvidence, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim evidence: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// RETURNING gives the claim and the read in one statement, which removes
	// the window a SELECT-then-UPDATE would leave open between choosing rows
	// and reserving them.
	//
	// The ORDER BY matches idx_evidence_pipeline_scan
	// (processing_stage, observed_at, id), so this is an index scan rather than
	// a sort of the whole table (DATA_MODEL.md §5).
	//
	// `attempts` increments here — at the moment the row is handed out, not at
	// the moment something fails. DATA_MODEL.md §7 names "mark processing
	// attempt" as its own operation; folding it into the claim makes it atomic
	// with the lock, and it is the only version that survives the failure worth
	// surviving. A row whose parser takes the whole process down with it — an
	// OOM, a SIGKILL — never reaches any code that could record a failure, and
	// if attempts only counted handled ones it would come back at zero forever,
	// retried on every pass for the rest of the database's life. Counting
	// hand-outs makes a poisonous artifact visible even when it is never
	// politely reported.
	rows, err := tx.QueryContext(ctx, `
		UPDATE evidence
		SET locked_until = ?, attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM evidence
			WHERE processing_stage = ?
			  AND (locked_until IS NULL OR locked_until <= ?)
			ORDER BY observed_at, id
			LIMIT ?
		)
		RETURNING id, raw_content, attempts`,
		formatTime(lockedUntil), stageReceived, formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim evidence: %w", err)
	}

	var pending []app.PendingEvidence
	for rows.Next() {
		var p app.PendingEvidence
		if err := rows.Scan(&p.ID, &p.RawContent, &p.Attempts); err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim evidence: scan: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("claim evidence: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("claim evidence: close: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim evidence: commit: %w", err)
	}
	return pending, nil
}

// MarkExtracted advances one row to EXTRACTED and drops its lock.
//
// It is for artifacts that produced no Claim. The ones that did are advanced
// inside ClaimRepository.Save, in the same transaction as the Claim itself,
// because a stage that moved without the Claim it was supposed to produce is a
// row Billy will never revisit and has nothing to show for.
//
// Guarded on the row still being at RECEIVED: a row some other pass already
// advanced is left alone rather than re-advanced.
func (q *EvidenceQueue) MarkExtracted(ctx context.Context, evidenceID string, now time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence
		SET processing_stage = ?, locked_until = NULL, last_error = NULL
		WHERE id = ? AND processing_stage = ?`,
		stageExtracted, evidenceID, stageReceived,
	); err != nil {
		return fmt.Errorf("mark evidence %s extracted: %w", evidenceID, err)
	}
	return nil
}

// RecordFailure stores a redacted reason and backs the row off until retryAt.
//
// The stage is untouched and the artifact is untouched. All that moves is
// last_error and locked_until — attempts already moved when the row was claimed.
// DATA_MODEL.md §7: retry is not a processing stage, and Evidence that failed
// extraction stays exactly where and what it was.
//
// `reason` arrives already redacted and is stored verbatim. Nothing here adds to
// it: this layer holds the artifact, and a store that enriched a diagnostic with
// what it knows about the row is how raw_content ends up in a column it was
// never supposed to reach (SECURITY.md §10).
func (q *EvidenceQueue) RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence
		SET last_error = ?, locked_until = ?
		WHERE id = ?`,
		reason, formatTime(retryAt), evidenceID,
	); err != nil {
		// The reason is redacted, but it is not this function's to re-emit —
		// the caller already has it and the row now holds it.
		return fmt.Errorf("record failure for evidence %s: %w", evidenceID, err)
	}
	return nil
}

// Release drops a lock without advancing the stage.
//
// The row stays at RECEIVED. Retry is not a processing stage (DATA_MODEL.md
// §7): a failed extraction leaves the artifact exactly where it was, and
// attempts, last_error and locked_until carry the retry behaviour instead.
func (q *EvidenceQueue) Release(ctx context.Context, evidenceID string, now time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence SET locked_until = NULL WHERE id = ?`,
		evidenceID,
	); err != nil {
		return fmt.Errorf("release evidence %s: %w", evidenceID, err)
	}
	return nil
}
