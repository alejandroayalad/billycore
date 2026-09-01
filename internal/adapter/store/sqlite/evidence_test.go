package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

var (
	observed   = time.Date(2026, 8, 16, 17, 44, 0, 0, time.UTC)
	ingestedAt = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
)

func newTestRepo(t *testing.T) (*EvidenceRepository, *sql.DB) {
	t.Helper()
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewEvidenceRepository(db), db
}

func newTestEvidence(t *testing.T, id, sourceReference string) domain.Evidence {
	t.Helper()
	e, err := domain.NewEvidence(
		id, "gmail_primary", domain.SourceGmail, sourceReference,
		"message/rfc822", []byte("From: nu@nu.com.mx\r\n\r\nTu transferencia fue exitosa"),
		observed,
	)
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	return e
}

// M1's idempotency requirement, provable with no network: the same Gmail
// message id ingested twice is one row, and the second call says so.
func TestInsertIsIdempotentOnSourceReference(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()

	first := newTestEvidence(t, "evidence-1", "18f2a9c4d5e6")
	created, err := repo.Insert(ctx, first, testProfile, ingestedAt)
	if err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if !created {
		t.Fatal("first Insert reported the artifact was already recorded")
	}

	// A re-sync fetches the same message again. Different Evidence id, because
	// the caller generated a new one — the identity that matters is
	// (source_id, source_reference), not the row id (D10).
	second := newTestEvidence(t, "evidence-2", "18f2a9c4d5e6")
	created, err = repo.Insert(ctx, second, testProfile, ingestedAt.Add(time.Hour))
	if err != nil {
		t.Fatalf("second Insert: %v", err)
	}
	if created {
		t.Error("second Insert reported a new row for an artifact already recorded")
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("evidence has %d rows, want 1", rows)
	}

	// The first artifact survives untouched. Evidence is never overwritten by a
	// re-fetch (D7, D10).
	var id string
	if err := db.QueryRow(`SELECT id FROM evidence`).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}
	if id != "evidence-1" {
		t.Errorf("stored id = %q, want evidence-1: the re-fetch overwrote the original", id)
	}
}

// D25: one ingestion, one event. A skipped duplicate emits nothing, or every
// re-sync would look like 1,044 new artifacts to every consumer.
func TestInsertEmitsOneEventPerIngestion(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, newTestEvidence(t, "evidence-1", "msg-a"), testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := repo.Insert(ctx, newTestEvidence(t, "evidence-2", "msg-a"), testProfile, ingestedAt); err != nil {
		t.Fatalf("duplicate Insert: %v", err)
	}

	var events int
	if err := db.QueryRow(`SELECT count(*) FROM domain_event WHERE type = ?`, eventEvidenceIngested).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Fatalf("domain_event has %d EvidenceIngested rows, want 1", events)
	}

	var payload, occurredAt string
	err := db.QueryRow(`SELECT payload, occurred_at FROM domain_event`).Scan(&payload, &occurredAt)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	want := `{"evidenceId":"evidence-1","sourceReference":"msg-a","observedAt":"2026-08-16T17:44:00.000Z"}`
	if payload != want {
		t.Errorf("payload = %s, want %s", payload, want)
	}
	if occurredAt != "2026-08-25T09:00:00.000Z" {
		t.Errorf("occurred_at = %q, want the ingestion time", occurredAt)
	}
}

// The event must not outlive a failed insert, and vice versa (DATA_MODEL.md §4.7).
func TestInsertWritesNoEventWhenTheRowFails(t *testing.T) {
	repo, db := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, newTestEvidence(t, "evidence-1", "msg-a"), testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// Same primary key, different source_reference: a PRIMARY KEY conflict, which
	// ON CONFLICT (source_id, source_reference) does not absorb.
	if _, err := repo.Insert(ctx, newTestEvidence(t, "evidence-1", "msg-b"), testProfile, ingestedAt); err == nil {
		t.Fatal("Insert accepted a duplicate primary key")
	}

	var events int
	if err := db.QueryRow(`SELECT count(*) FROM domain_event`).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if events != 1 {
		t.Errorf("domain_event has %d rows, want 1: a failed insert left an event behind", events)
	}
}

func TestGetByIDReturnsTheStoredArtifact(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	stored := newTestEvidence(t, "evidence-1", "18f2a9c4d5e6")
	if _, err := repo.Insert(ctx, stored, testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByID(ctx, "evidence-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID() != stored.ID() ||
		got.SourceID() != stored.SourceID() ||
		got.SourceType() != stored.SourceType() ||
		got.SourceReference() != stored.SourceReference() ||
		got.ContentType() != stored.ContentType() {
		t.Errorf("round trip changed the Evidence: got %+v", got)
	}
	if !bytes.Equal(got.RawContent(), stored.RawContent()) {
		t.Error("raw_content did not round trip verbatim")
	}
	if !got.ObservedAt().Equal(stored.ObservedAt()) {
		t.Errorf("observed_at = %s, want %s", got.ObservedAt(), stored.ObservedAt())
	}
}

func TestGetByIDReportsMissingEvidence(t *testing.T) {
	repo, _ := newTestRepo(t)
	_, err := repo.GetByID(context.Background(), "no-such-evidence")
	if !errors.Is(err, ErrEvidenceNotFound) {
		t.Errorf("err = %v, want ErrEvidenceNotFound", err)
	}
}

func TestGetByReferenceReturnsTheStoredArtifact(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()
	stored := newTestEvidence(t, "evidence-1", "statement.pdf")
	if _, err := repo.Insert(ctx, stored, testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := repo.GetByReference(ctx, "gmail_primary", "statement.pdf")
	if err != nil {
		t.Fatalf("GetByReference: %v", err)
	}
	if got.ID() != "evidence-1" || !bytes.Equal(got.RawContent(), stored.RawContent()) {
		t.Errorf("GetByReference returned %+v", got)
	}
	if _, err := repo.GetByReference(ctx, "other", "statement.pdf"); !errors.Is(err, ErrEvidenceNotFound) {
		t.Errorf("missing reference error = %v", err)
	}
}

// Ingestion records and stops. Extraction is a separate stage (D7).
func TestInsertLeavesEvidenceAtReceived(t *testing.T) {
	repo, db := newTestRepo(t)
	if _, err := repo.Insert(context.Background(), newTestEvidence(t, "evidence-1", "msg-a"), testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var stage string
	var attempts int
	var lastError, lockedUntil sql.NullString
	err := db.QueryRow(`SELECT processing_stage, attempts, last_error, locked_until FROM evidence`).
		Scan(&stage, &attempts, &lastError, &lockedUntil)
	if err != nil {
		t.Fatalf("read pipeline columns: %v", err)
	}
	if stage != stageReceived {
		t.Errorf("processing_stage = %q, want RECEIVED", stage)
	}
	if attempts != 0 || lastError.Valid || lockedUntil.Valid {
		t.Errorf("pipeline state is not clean: attempts=%d last_error=%v locked_until=%v", attempts, lastError, lockedUntil)
	}
}

// Absent and empty are different facts (DATA_MODEL.md §2).
func TestInsertStoresMissingContentTypeAsNull(t *testing.T) {
	repo, db := newTestRepo(t)

	e, err := domain.NewEvidence("evidence-1", "gmail_primary", domain.SourceGmail, "msg-a", "", nil, observed)
	if err != nil {
		t.Fatalf("NewEvidence: %v", err)
	}
	if _, err := repo.Insert(context.Background(), e, testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	var contentType, rawContent sql.NullString
	if err := db.QueryRow(`SELECT content_type, raw_content FROM evidence`).Scan(&contentType, &rawContent); err != nil {
		t.Fatalf("select: %v", err)
	}
	if contentType.Valid {
		t.Errorf("content_type = %q, want NULL", contentType.String)
	}
	if rawContent.Valid {
		t.Error("raw_content is not NULL: a reference-only artifact stored empty bytes instead")
	}
}

func TestExistsByReference(t *testing.T) {
	repo, _ := newTestRepo(t)
	ctx := context.Background()

	if _, err := repo.Insert(ctx, newTestEvidence(t, "evidence-1", "msg-a"), testProfile, ingestedAt); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	cases := []struct {
		name                      string
		sourceID, sourceReference string
		want                      bool
	}{
		{"recorded", "gmail_primary", "msg-a", true},
		{"different reference", "gmail_primary", "msg-b", false},
		// Identity is the pair, not the reference alone (D10): the same message
		// id in a second mailbox is a different artifact.
		{"same reference, other source", "gmail_secondary", "msg-a", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ExistsByReference(ctx, tc.sourceID, tc.sourceReference)
			if err != nil {
				t.Fatalf("ExistsByReference: %v", err)
			}
			if got != tc.want {
				t.Errorf("ExistsByReference(%q, %q) = %v, want %v", tc.sourceID, tc.sourceReference, got, tc.want)
			}
		})
	}
}
