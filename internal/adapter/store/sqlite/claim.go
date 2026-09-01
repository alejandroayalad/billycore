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

// Save writes one complete interpretation in one database transaction: each
// Claim, its provenance, its fields, one event for each Claim, the pointer, and
// the stage advance. One commit, for the reason in D25. The set is atomic
// (D46). A false with a nil error means that another pass wrote first, and the
// table evidence_active_interpretation makes that idempotent, not a check here.
func (r *ClaimRepository) Save(ctx context.Context, in domain.Interpretation, profile app.ExtractionProfile, now time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("save interpretation: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO interpretations (id, evidence_id, superseded_by_interpretation_id, created_at, extraction_profile, unread_rows)
		VALUES (?, ?, NULL, ?, ?, ?)`,
		in.ID(), in.EvidenceID(), formatTime(in.CreatedAt()), string(profile), in.UnreadRows(),
	); err != nil {
		return false, fmt.Errorf("save interpretation %s: %w", in.ID(), err)
	}

	for _, c := range in.Claims() {
		if err := insertClaim(ctx, tx, in, c, now); err != nil {
			return false, err
		}
	}

	// The constraint. The Save that takes the active slot is the Save that
	// counts. To lose is an ordinary result and not an error.
	took, err := takeActiveSlot(ctx, tx, in)
	if err != nil {
		return false, err
	}
	if !took {
		// Another pass wrote first. The deferred Rollback discards each Claim,
		// its fields, its provenance, its event, and the interpretation row.
		return false, nil
	}

	if in.Supersedes() != "" {
		if err := retireInterpretation(ctx, tx, in, now); err != nil {
			return false, err
		}
	}

	// The stage advance. The guard on RECEIVED stops a second advance of a row
	// that another pass moved. The same statement drops the lock and clears
	// last_error, because a row that failed and then succeeded must not keep an
	// old message beside a good Claim.
	if _, err := tx.ExecContext(ctx, `
		UPDATE evidence
		SET processing_stage = ?, locked_until = NULL, last_error = NULL
		WHERE id = ? AND processing_stage = ?`,
		stageExtracted, in.EvidenceID(), stageReceived,
	); err != nil {
		return false, fmt.Errorf("save interpretation %s: advance evidence %s: %w", in.ID(), in.EvidenceID(), err)
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("save interpretation %s: commit: %w", in.ID(), err)
	}
	return true, nil
}

// insertClaim writes one member of an interpretation: the row, its provenance,
// its fields, its membership of the set, and its activation event.
func insertClaim(ctx context.Context, tx *sql.Tx, in domain.Interpretation, c domain.Claim, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO claims (id, state, superseded_by_claim_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)`,
		c.ID(), string(c.State()), nullableText(c.SupersededBy()),
		formatTime(c.CreatedAt()), formatTime(c.UpdatedAt()),
	); err != nil {
		return fmt.Errorf("save claim %s: %w", c.ID(), err)
	}

	// Write the provenance first. A Claim without provenance is invalid
	// (DOMAIN.md §4), and the foreign key to evidence enforces that.
	for _, evidenceID := range c.EvidenceIDs() {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO claim_evidence (claim_id, evidence_id) VALUES (?, ?)`,
			c.ID(), evidenceID,
		); err != nil {
			return fmt.Errorf("save claim %s: provenance: %w", c.ID(), err)
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
			// The field name is safe to log. The value is not, because a
			// merchant can be the name of a person (SECURITY.md §10).
			return fmt.Errorf("save claim %s: field %s: %w", c.ID(), name, err)
		}
	}

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO interpretation_claims (interpretation_id, claim_id) VALUES (?, ?)`,
		in.ID(), c.ID(),
	); err != nil {
		return fmt.Errorf("save claim %s: membership of interpretation %s: %w", c.ID(), in.ID(), err)
	}

	return appendClaimActivated(ctx, tx, in, c, now)
}

// takeActiveSlot makes this interpretation the one that Billy uses, and reports
// if it took the slot. A first reading uses INSERT. A replacement uses UPDATE
// with the interpretation that it expects to find. This compare-and-swap lets
// the database separate a planned replacement from a lost race (D47).
func takeActiveSlot(ctx context.Context, tx *sql.Tx, in domain.Interpretation) (bool, error) {
	var res sql.Result
	var err error
	if in.Supersedes() == "" {
		res, err = tx.ExecContext(ctx, `
			INSERT INTO evidence_active_interpretation (evidence_id, interpretation_id)
			VALUES (?, ?)
			ON CONFLICT (evidence_id) DO NOTHING`,
			in.EvidenceID(), in.ID(),
		)
	} else {
		res, err = tx.ExecContext(ctx, `
			UPDATE evidence_active_interpretation
			SET interpretation_id = ?
			WHERE evidence_id = ? AND interpretation_id = ?`,
			in.ID(), in.EvidenceID(), in.Supersedes(),
		)
	}
	if err != nil {
		return false, fmt.Errorf("save interpretation %s: claim the active slot for evidence %s: %w", in.ID(), in.EvidenceID(), err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("save interpretation %s: rows affected: %w", in.ID(), err)
	}
	return affected == 1, nil
}

// retireInterpretation records the lineage from the old set to this one (D47),
// moves the old Claims to SUPERSEDED, and retires their Transactions (D49). It
// does not write superseded_by_transaction_id: the replacements do not exist
// yet, and the two sets can differ in size. Each statement has a guard on the
// current state, so it does not change a row that another pass already moved.
func retireInterpretation(ctx context.Context, tx *sql.Tx, in domain.Interpretation, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE interpretations
		SET superseded_by_interpretation_id = ?
		WHERE id = ?`,
		in.ID(), in.Supersedes(),
	); err != nil {
		return fmt.Errorf("save interpretation %s: record lineage from %s: %w", in.ID(), in.Supersedes(), err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE claims
		SET state = ?, updated_at = ?
		WHERE state = ?
		  AND id IN (SELECT claim_id FROM interpretation_claims WHERE interpretation_id = ?)`,
		string(domain.ClaimSuperseded), formatTime(now), string(domain.ClaimActive), in.Supersedes(),
	); err != nil {
		return fmt.Errorf("save interpretation %s: supersede the claims of %s: %w", in.ID(), in.Supersedes(), err)
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE transactions
		SET transaction_state = ?, updated_at = ?
		WHERE transaction_state = ?
		  AND id IN (
			SELECT ct.transaction_id
			FROM claim_transaction ct
			JOIN interpretation_claims ic ON ic.claim_id = ct.claim_id
			WHERE ic.interpretation_id = ?
		  )`,
		string(domain.TransactionSuperseded), formatTime(now), string(domain.TransactionActive), in.Supersedes(),
	); err != nil {
		return fmt.Errorf("save interpretation %s: retire the transactions of %s: %w", in.ID(), in.Supersedes(), err)
	}
	return nil
}

// appendClaimActivated writes the event in DOMAIN.md §8. The payload holds ids
// and no interpreted values (SECURITY.md §10). It names the interpretation of
// the Claim and the one that it replaces, because D47 records lineage for each
// set and not for each Claim. DOMAIN.md §8 does not list those two fields yet.
func appendClaimActivated(ctx context.Context, tx *sql.Tx, in domain.Interpretation, c domain.Claim, now time.Time) error {
	if c.State() != domain.ClaimActive {
		return nil
	}
	evidenceIDs := c.EvidenceIDs()
	payload, err := json.Marshal(struct {
		ClaimID                    string   `json:"claimId"`
		EvidenceIDs                []string `json:"evidenceIds"`
		InterpretationID           string   `json:"interpretationId"`
		SupersedesInterpretationID string   `json:"supersedesInterpretationId,omitempty"`
		SupersededClaimID          string   `json:"supersededClaimId,omitempty"`
	}{
		ClaimID:                    c.ID(),
		EvidenceIDs:                evidenceIDs,
		InterpretationID:           in.ID(),
		SupersedesInterpretationID: in.Supersedes(),
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

// querier is the read part of *sql.DB and of *sql.Tx. It lets loadClaim read a
// Claim inside a transaction or outside one.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// loadClaim reads one Claim back into the domain: the row, its provenance and
// its fields. It builds the Claim through domain.NewClaim and Activate, because
// the schema has no CHECK constraints (migration 002). PROPOSED and then
// Activate keeps created_at and updated_at. It reads only ACTIVE Claims.
func loadClaim(ctx context.Context, q querier, claimID string) (domain.Claim, error) {
	var state, createdAt, updatedAt string
	err := q.QueryRowContext(ctx, `
		SELECT state, created_at, updated_at FROM claims WHERE id = ?`,
		claimID,
	).Scan(&state, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		// The foreign key from interpretation_claims prevents this, except
		// after corruption. A clear error matters most in that case.
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
		// The CHECK in migration 002 guarantees that exactly one value is
		// present. NewIntField and NewTextField apply the domain rules again.
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
			// The field name is safe to log. The value is not (SECURITY.md §10).
			return nil, fmt.Errorf("load claim %s: field %s: %w", claimID, name, err)
		}
		fields[domain.FieldName(name)] = field
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load claim %s: fields: %w", claimID, err)
	}
	return fields, nil
}
