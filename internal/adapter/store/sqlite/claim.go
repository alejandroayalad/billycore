package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// stageExtracted is where extraction leaves Evidence (ARCHITECTURE.md §5).
const stageExtracted = "EXTRACTED"

// eventClaimActivated is the event name from DOMAIN.md §8.
const eventClaimActivated = "ClaimActivated"

// ClaimRepository stores Claims.
type ClaimRepository struct {
	db *sql.DB
}

func NewClaimRepository(db *sql.DB) *ClaimRepository {
	return &ClaimRepository{db: db}
}

// Save writes a Claim, its provenance, its fields, its ClaimActivated event and
// the Evidence stage advance — all in one transaction — and reports whether a
// Claim was created.
//
// One commit, for D25's reason applied twice over. An event describing a change
// that did not commit is a lie about the domain (DATA_MODEL.md §4.7). And
// Evidence advanced to EXTRACTED whose Claim rolled back is a worse lie: the
// pipeline would never look at that artifact again and would have nothing to
// show for it — 51 MB of email quietly reduced to a table that is missing rows
// nobody can name.
//
// **A false with a nil error means the Evidence already has an active
// interpretation**, and this one was not written. That is the same contract
// EvidenceRepository.Insert offers for a re-synced artifact, and it comes from
// the same place: a constraint. `evidence_active_claim`'s primary key is what
// makes re-running extraction idempotent, not a check this function remembered
// to perform — see migration 003 for the race it closes.
//
// `now` is passed in rather than read from the clock, so the caller owns time.
func (r *ClaimRepository) Save(ctx context.Context, c domain.Claim, now time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("save claim: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO claims (id, state, superseded_by_claim_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		c.ID(), string(c.State()), nullableText(c.SupersededBy()),
		formatTime(c.CreatedAt()), formatTime(c.UpdatedAt()),
	); err != nil {
		return false, fmt.Errorf("save claim %s: %w", c.ID(), err)
	}

	// Provenance first among the children: a Claim without it is invalid
	// (DOMAIN.md §4), and the foreign key to evidence is what makes that
	// unfakeable rather than merely asserted.
	for _, evidenceID := range c.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_evidence (claim_id, evidence_id) VALUES (?, ?)`,
			c.ID(), evidenceID,
		); err != nil {
			return false, fmt.Errorf("save claim %s: provenance: %w", c.ID(), err)
		}
	}

	for _, name := range c.FieldNames() {
		field, ok := c.Field(name)
		if !ok {
			continue // unreachable: FieldNames lists what the Claim holds
		}
		var valueInt, valueText any
		if field.IsInt() {
			valueInt = field.Int()
		} else {
			valueText = field.Text()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_fields (claim_id, field_name, value_int, value_text, confidence)
			VALUES (?, ?, ?, ?, ?)`,
			c.ID(), string(name), valueInt, valueText, string(field.Confidence()),
		); err != nil {
			// The field name is safe to log; the value is not necessarily —
			// a merchant is a person's name (SECURITY.md §10).
			return false, fmt.Errorf("save claim %s: field %s: %w", c.ID(), name, err)
		}
	}

	if err := appendClaimActivated(ctx, tx, c, now); err != nil {
		return false, err
	}

	// The constraint. Claiming the active slot is what makes this Save the one
	// that counts, and losing the race is an ordinary outcome rather than an
	// error: the artifact already has an interpretation, and a second identical
	// one is not additional knowledge.
	//
	// ON CONFLICT DO NOTHING rather than catching a violation, so idempotency
	// never depends on matching the text of a driver's error message.
	//
	// It runs only for an ACTIVE Claim. A PROPOSED one — what POST /v1/claims
	// will write for an outside proposer (D11, D12) — takes no slot and
	// conflicts with nothing.
	if c.State() == domain.ClaimActive {
		for _, evidenceID := range c.EvidenceIDs() {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO evidence_active_claim (evidence_id, claim_id)
				VALUES (?, ?)
				ON CONFLICT (evidence_id) DO NOTHING`,
				evidenceID, c.ID(),
			)
			if err != nil {
				return false, fmt.Errorf("save claim %s: claim the active slot for evidence %s: %w", c.ID(), evidenceID, err)
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return false, fmt.Errorf("save claim %s: rows affected: %w", c.ID(), err)
			}
			if affected == 0 {
				// Another interpretation got there first. Nothing written here
				// survives: the deferred Rollback discards the Claim, its
				// fields, its provenance and its event together, so there is no
				// half-recorded interpretation and no event describing one.
				return false, nil
			}
		}
	}

	// The stage advance. Guarded on the row still being at RECEIVED so that a
	// row already advanced by another pass is not silently re-advanced; the
	// lock is dropped in the same statement, because work that committed is
	// work nobody needs to retry.
	//
	// last_error is cleared with it. A row that failed on Monday and succeeded
	// on Tuesday must not keep Monday's diagnostic: a stale last_error beside a
	// perfectly good Claim reads as a current problem, and the whole point of
	// the column is that someone looks at it.
	for _, evidenceID := range c.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			UPDATE evidence
			SET processing_stage = ?, locked_until = NULL, last_error = NULL
			WHERE id = ? AND processing_stage = ?`,
			stageExtracted, evidenceID, stageReceived,
		); err != nil {
			return false, fmt.Errorf("save claim %s: advance evidence %s: %w", c.ID(), evidenceID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("save claim %s: commit: %w", c.ID(), err)
	}
	return true, nil
}

// appendClaimActivated writes the event described in DOMAIN.md §8: Billy's
// current usable interpretation of some Evidence changed.
//
// The payload carries ids, not interpreted values. It is a fact about an
// interpretation, not a second copy of one (SECURITY.md §10) — and a consumer
// that wants the fields reads the Claim.
//
// It is written only for a Claim that is ACTIVE. A Claim that stopped at
// PROPOSED has not become anyone's interpretation of anything, and an event
// saying otherwise would make the log describe an activation that did not
// happen.
func appendClaimActivated(ctx context.Context, tx *sql.Tx, c domain.Claim, now time.Time) error {
	if c.State() != domain.ClaimActive {
		return nil
	}
	evidenceIDs := c.EvidenceIDs()
	payload, err := json.Marshal(struct {
		ClaimID           string   `json:"claimId"`
		EvidenceIDs       []string `json:"evidenceIds"`
		SupersededClaimID string   `json:"supersededClaimId,omitempty"`
	}{
		ClaimID:     c.ID(),
		EvidenceIDs: evidenceIDs,
	})
	if err != nil {
		return fmt.Errorf("%s: marshal payload: %w", eventClaimActivated, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO domain_event (type, payload, occurred_at) VALUES (?, ?, ?)`,
		eventClaimActivated, string(payload), formatTime(now),
	); err != nil {
		return fmt.Errorf("%s: %w", eventClaimActivated, err)
	}
	return nil
}

// querier is what loadClaim needs: the read half of *sql.DB and *sql.Tx alike,
// so a Claim can be read inside someone else's transaction or outside one.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadClaim reads one Claim back into the domain: the row, its provenance and
// its fields.
//
// **It rehydrates through the domain constructor and the ordinary transition,**
// rather than assembling a Claim struct directly — which the domain does not
// permit anyway, and should not. Everything in the database was validated on
// the way in, but a schema with no CHECK constraints (migration 002, by design)
// means the database is not what makes it valid; domain.NewClaim is. A row hand-
// edited to say `direction = 'SIDEWAYS'` fails here instead of becoming a
// Transaction.
//
// PROPOSED and then Activate rather than constructing at ACTIVE, so that
// created_at and updated_at both survive the round trip. Constructing at ACTIVE
// would set updated_at to created_at and quietly lose the difference for any
// Claim that was activated later than it was proposed — which a Claim arriving
// through POST /v1/claims may well be (D11, D12).
//
// It reads only ACTIVE Claims, because the only caller finds them through
// `evidence_active_claim`, and it says so rather than assuming it.
func loadClaim(ctx context.Context, q querier, claimID string) (domain.Claim, error) {
	var state, createdAt, updatedAt string
	err := q.QueryRowContext(ctx, `
		SELECT state, created_at, updated_at FROM claims WHERE id = ?`,
		claimID,
	).Scan(&state, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// The foreign key from evidence_active_claim makes this unreachable
		// short of corruption, which is exactly when a clear error matters.
		return domain.Claim{}, fmt.Errorf("load claim %s: no such claim", claimID)
	}
	if err != nil {
		return domain.Claim{}, fmt.Errorf("load claim %s: %w", claimID, err)
	}
	if domain.ClaimState(state) != domain.ClaimActive {
		return domain.Claim{}, fmt.Errorf("load claim %s: state is %s, and only an ACTIVE claim is loaded here", claimID, state)
	}

	created, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("load claim %s: created_at: %w", claimID, err)
	}
	updated, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("load claim %s: updated_at: %w", claimID, err)
	}

	evidenceIDs, err := loadClaimProvenance(ctx, q, claimID)
	if err != nil {
		return domain.Claim{}, err
	}
	fields, err := loadClaimFields(ctx, q, claimID)
	if err != nil {
		return domain.Claim{}, err
	}

	proposed, err := domain.NewClaim(claimID, domain.ClaimProposed, evidenceIDs, fields, created)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("load claim %s: %w", claimID, err)
	}
	active, err := proposed.Activate(updated)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("load claim %s: activate: %w", claimID, err)
	}
	return active, nil
}

func loadClaimProvenance(ctx context.Context, q querier, claimID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT evidence_id FROM claim_evidence WHERE claim_id = ? ORDER BY evidence_id`,
		claimID,
	)
	if err != nil {
		return nil, fmt.Errorf("load claim %s: provenance: %w", claimID, err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("load claim %s: provenance: %w", claimID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load claim %s: provenance: %w", claimID, err)
	}
	return ids, nil
}

func loadClaimFields(ctx context.Context, q querier, claimID string) (map[domain.FieldName]domain.ClaimField, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT field_name, value_int, value_text, confidence
		FROM claim_fields WHERE claim_id = ?`,
		claimID,
	)
	if err != nil {
		return nil, fmt.Errorf("load claim %s: fields: %w", claimID, err)
	}
	defer rows.Close()

	fields := map[domain.FieldName]domain.ClaimField{}
	for rows.Next() {
		var name, confidence string
		var valueInt sql.NullInt64
		var valueText sql.NullString
		if err := rows.Scan(&name, &valueInt, &valueText, &confidence); err != nil {
			return nil, fmt.Errorf("load claim %s: fields: %w", claimID, err)
		}
		// The CHECK in migration 002 guarantees exactly one of the two is
		// present. Which one it is decides the field's shape, and NewIntField /
		// NewTextField re-apply the domain's rules to it.
		var field domain.ClaimField
		switch {
		case valueInt.Valid:
			field, err = domain.NewIntField(valueInt.Int64, domain.Confidence(confidence))
		case valueText.Valid:
			field, err = domain.NewTextField(valueText.String, domain.Confidence(confidence))
		default:
			err = errors.New("neither an integer nor a text value")
		}
		if err != nil {
			// The field name is safe to log; the value is not (SECURITY.md §10).
			return nil, fmt.Errorf("load claim %s: field %s: %w", claimID, name, err)
		}
		fields[domain.FieldName(name)] = field
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load claim %s: fields: %w", claimID, err)
	}
	return fields, nil
}
