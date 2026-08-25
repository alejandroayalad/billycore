package domain

import (
	"testing"
	"time"
)

func validEvidence(t *testing.T) Evidence {
	t.Helper()
	e, err := NewEvidence("ev1", "gmail_primary", SourceGmail, "18f2a9c4d1e77b03",
		"message/rfc822", []byte("From: nu@nu.com.mx\r\n\r\nMonto: $299.00"),
		time.Date(2026, 8, 18, 18, 54, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("valid Evidence rejected: %v", err)
	}
	return e
}

func TestNewEvidenceRequiresIdentity(t *testing.T) {
	at := time.Now()
	body := []byte("x")
	cases := map[string]struct {
		id, sourceID string
		typ          SourceType
		ref          string
		observedAt   time.Time
	}{
		"no id":               {"", "gmail_primary", SourceGmail, "m1", at},
		"no source id":        {"ev1", "", SourceGmail, "m1", at},
		"no source reference": {"ev1", "gmail_primary", SourceGmail, "", at},
		"unknown source type": {"ev1", "gmail_primary", "TELEPATHY", "m1", at},
		"zero observed_at":    {"ev1", "gmail_primary", SourceGmail, "m1", time.Time{}},
	}
	for name, c := range cases {
		if _, err := NewEvidence(c.id, c.sourceID, c.typ, c.ref, "text/plain", body, c.observedAt); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// DATA_MODEL.md §4.1: raw_content may be NULL when source_reference is itself
// the preserved reference to the artifact.
func TestNewEvidenceAcceptsNilRawContent(t *testing.T) {
	e, err := NewEvidence("ev1", "manual", SourceBankStatement, "statement-2026-07.pdf",
		"application/pdf", nil, time.Now())
	if err != nil {
		t.Fatalf("nil raw content rejected: %v", err)
	}
	if e.HasRawContent() {
		t.Error("HasRawContent true for nil content")
	}
	if e.ContentBytes() != 0 {
		t.Errorf("ContentBytes = %d, want 0", e.ContentBytes())
	}
}

// The immutability invariant is only real if a caller cannot reach the bytes.
func TestRecordedEvidenceCannotBeMutatedThroughTheCallersSlice(t *testing.T) {
	body := []byte("Monto: $299.00")
	e, err := NewEvidence("ev1", "gmail_primary", SourceGmail, "m1", "message/rfc822", body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	body[0] = 'X' // the caller still holds its slice
	if got := string(e.RawContent()); got != "Monto: $299.00" {
		t.Fatalf("recorded Evidence changed to %q when the caller mutated its own slice", got)
	}
}

func TestRecordedEvidenceCannotBeMutatedThroughTheReturnedSlice(t *testing.T) {
	e := validEvidence(t)
	before := string(e.RawContent())
	got := e.RawContent()
	got[0] = 'X'
	if after := string(e.RawContent()); after != before {
		t.Fatalf("recorded Evidence changed to %q via the slice RawContent handed out", after)
	}
}

func TestObservedAtIsNormalisedToUTC(t *testing.T) {
	mx := time.FixedZone("CST", -6*3600)
	local := time.Date(2026, 8, 18, 12, 54, 0, 0, mx)
	e, err := NewEvidence("ev1", "gmail_primary", SourceGmail, "m1", "message/rfc822", nil, local)
	if err != nil {
		t.Fatal(err)
	}
	if loc := e.ObservedAt().Location(); loc != time.UTC {
		t.Errorf("observed_at kept location %v; DATA_MODEL.md §2 stores UTC", loc)
	}
	if !e.ObservedAt().Equal(local) {
		t.Error("normalising to UTC changed the instant")
	}
}

func TestAccessors(t *testing.T) {
	e := validEvidence(t)
	if e.ID() != "ev1" || e.SourceID() != "gmail_primary" ||
		e.SourceType() != SourceGmail || e.SourceReference() != "18f2a9c4d1e77b03" ||
		e.ContentType() != "message/rfc822" {
		t.Fatalf("accessor mismatch: %+v", e)
	}
	if !e.HasRawContent() || e.ContentBytes() != len(e.RawContent()) {
		t.Error("content accessors disagree")
	}
}
