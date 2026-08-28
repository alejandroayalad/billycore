package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
)

// EvidenceQueue moves Evidence rows through the pipeline. It writes the four
// columns in DATA_MODEL.md §7: processing_stage, attempts, last_error and
// locked_until. It is separate from EvidenceRepository, because those columns
// are not part of the Evidence domain object.
type EvidenceQueue struct {
	db *sql.DB
}

func NewEvidenceQueue(db *sql.DB) *EvidenceQueue {
	return &EvidenceQueue{db: db}
}

// ClaimForExtraction locks up to limit rows at stage RECEIVED and returns them.
// The lock and the read are one transaction, so two passes cannot take one row.
// A lock that ended is available again, which makes a stopped process
// recoverable without an operator.
func (q *EvidenceQueue) ClaimForExtraction(ctx context.Context, limit int, now, lockedUntil time.Time) ([]app.PendingEvidence, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim evidence: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// RETURNING does the lock and the read in one statement. The ORDER BY
	// matches idx_evidence_pipeline_scan (DATA_MODEL.md §5). The count in
	// attempts increases when the queue gives out the row, not when work fails:
	// an artifact that stops the process never reaches code that reports a
	// failure, and it would return to each pass for ever.
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

	// Read what Billy believes about each row after the cursor closes, because
	// SQLite allows one statement at a time on a connection. The value is empty
	// for a row that no parser has read. It has a value for a row that an
	// operator reset for re-extraction (D48).
	for i := range pending {
		interpretationID, err := activeInterpretationID(ctx, tx, pending[i].ID)
		if err != nil {
			return nil, err
		}
		pending[i].ActiveInterpretationID = interpretationID
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim evidence: commit: %w", err)
	}
	return pending, nil
}

// MarkExtracted advances one row to stage EXTRACTED and drops its lock. It is
// for an artifact that gave no Claim: ClaimRepository.Save advances the others
// with their interpretation, because a row that advances without its Claims is
// a row that Billy never reads again. The guard on RECEIVED prevents a second
// advance.
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

// RecordFailure stores a redacted reason and delays the row until retryAt. The
// stage and the artifact do not change, because retry is not a processing stage
// (DATA_MODEL.md §7). It stores `reason` without change: a store that added
// what it knows would put raw_content in a column (SECURITY.md §10).
func (q *EvidenceQueue) RecordFailure(ctx context.Context, evidenceID, reason string, retryAt time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence
		SET last_error = ?, locked_until = ?
		WHERE id = ?`,
		reason, formatTime(retryAt), evidenceID,
	); err != nil {
		// Do not repeat the reason here. The caller has it, and the row holds
		// it.
		return fmt.Errorf("record failure for evidence %s: %w", evidenceID, err)
	}
	return nil
}

// Release drops a lock and does not advance the stage. The row stays at
// RECEIVED, because retry is not a processing stage (DATA_MODEL.md §7). The
// columns attempts, last_error and locked_until hold the retry state.
func (q *EvidenceQueue) Release(ctx context.Context, evidenceID string, now time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence SET locked_until = NULL WHERE id = ?`,
		evidenceID,
	); err != nil {
		return fmt.Errorf("release evidence %s: %w", evidenceID, err)
	}
	return nil
}

// ClaimForReconciliation locks up to limit rows at stage EXTRACTED and returns
// each with the Claims of its active interpretation. The lock and the read are
// one transaction, for the reason that ClaimForExtraction gives. It returns a
// row that has no Claims: 244 artifacts have none, and they must leave the
// queue (D44).
func (q *EvidenceQueue) ClaimForReconciliation(ctx context.Context, limit int, now, lockedUntil time.Time) ([]app.PendingReconciliation, error) {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim for reconciliation: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	// The same lock and read as ClaimForExtraction, for the next stage. The row
	// carries observed_at, because the fallback in DATA_MODEL.md §4.5 needs it.
	// The count in attempts does not reset between stages: it counts how many
	// times the pipeline gave out this row, at any stage.
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
		RETURNING id, observed_at, attempts`,
		formatTime(lockedUntil), stageExtracted, formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("claim for reconciliation: %w", err)
	}

	var pending []app.PendingReconciliation
	for rows.Next() {
		var work app.PendingReconciliation
		var observedAt string
		if err := rows.Scan(&work.EvidenceID, &observedAt, &work.Attempts); err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim for reconciliation: scan: %w", err)
		}
		work.ObservedAt, err = time.Parse(time.RFC3339, observedAt)
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("claim for reconciliation: evidence %s: observed_at: %w", work.EvidenceID, err)
		}
		pending = append(pending, work)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("claim for reconciliation: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("claim for reconciliation: close: %w", err)
	}

	// Read the Claims after the cursor closes. SQLite allows one statement at a
	// time on a connection, so a read during RETURNING would block against the
	// open cursor.
	for i := range pending {
		claimIDs, err := activeClaimIDs(ctx, tx, pending[i].EvidenceID)
		if err != nil {
			return nil, err
		}
		for _, claimID := range claimIDs {
			claim, err := loadClaim(ctx, tx, claimID)
			if err != nil {
				return nil, err
			}
			pending[i].Claims = append(pending[i].Claims, claim)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim for reconciliation: commit: %w", err)
	}
	return pending, nil
}

// activeInterpretationID returns the reading that Billy uses for one artifact,
// or "" if no parser has read it. The primary key of
// evidence_active_interpretation allows one row for each artifact (D38, D47),
// so this function needs no rule to choose between two readings.
func activeInterpretationID(ctx context.Context, q querier, evidenceID string) (string, error) {
	var interpretationID string
	err := q.QueryRowContext(ctx, `
		SELECT interpretation_id FROM evidence_active_interpretation WHERE evidence_id = ?`,
		evidenceID,
	).Scan(&interpretationID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("active interpretation for evidence %s: %w", evidenceID, err)
	}
	return interpretationID, nil
}

// activeClaimIDs returns each Claim in the active interpretation of the
// artifact. It returns one row for an email, and one row for each movement of a
// statement (D46). No code depends on the order by claim id, but a stable order
// makes a test fail in the same way each time.
func activeClaimIDs(ctx context.Context, q querier, evidenceID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT ic.claim_id
		FROM evidence_active_interpretation eai
		JOIN interpretation_claims ic ON ic.interpretation_id = eai.interpretation_id
		WHERE eai.evidence_id = ?
		ORDER BY ic.claim_id`,
		evidenceID,
	)
	if err != nil {
		return nil, fmt.Errorf("active claims for evidence %s: %w", evidenceID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("active claims for evidence %s: %w", evidenceID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("active claims for evidence %s: %w", evidenceID, err)
	}
	return ids, nil
}

// MarkReconciled advances one row to stage RECONCILED and drops its lock. It is
// for an artifact with no Claims, and for one whose Transactions another pass
// wrote. TransactionRepository.Save advances the others with their
// Transactions. The guard on EXTRACTED prevents a second advance.
func (q *EvidenceQueue) MarkReconciled(ctx context.Context, evidenceID string, now time.Time) error {
	if _, err := q.db.ExecContext(ctx, `
		UPDATE evidence
		SET processing_stage = ?, locked_until = NULL, last_error = NULL
		WHERE id = ? AND processing_stage = ?`,
		stageReconciled, evidenceID, stageExtracted,
	); err != nil {
		return fmt.Errorf("mark evidence %s reconciled: %w", evidenceID, err)
	}
	return nil
}
