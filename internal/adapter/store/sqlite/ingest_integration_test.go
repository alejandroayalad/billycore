package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/app"
	"github.com/alejandroayalad/billycore/internal/domain"
)

// countingFetcher stands in for a mailbox. Nothing here touches the network.
type countingFetcher struct {
	references []string
	fetches    int
}

func (f *countingFetcher) ListReferences(context.Context) ([]string, error) { return f.references, nil }

func (f *countingFetcher) Fetch(_ context.Context, reference string) (app.Artifact, error) {
	f.fetches++
	return app.Artifact{
		Reference:   reference,
		ContentType: "message/rfc822",
		ObservedAt:  observed,
		Content:     []byte("From: nu@nu.com.mx\r\nSubject: Tu transferencia fue exitosa\r\n\r\n" + reference),
	}, nil
}

// M1's acceptance, run against real storage: sync twice, and the second run
// creates nothing. The mailbox here is 20 artifacts rather than 1,044, which
// changes the numbers and not the property being proved.
func TestSyncTwiceAgainstRealStorage(t *testing.T) {
	db, err := Open(testDBPath(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	references := make([]string, 20)
	for i := range references {
		references[i] = "msg-" + time.Duration(i).String()
	}
	fetcher := &countingFetcher{references: references}

	ingestor := ingestorFor(NewEvidenceRepository(db))
	source := app.Source{ID: "gmail_primary", Type: domain.SourceGmail, Profile: testProfile}

	first, err := ingestor.Sync(context.Background(), source, fetcher)
	if err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if first.ArtifactsDiscovered != 20 || first.EvidenceCreated != 20 || first.EvidenceSkipped != 0 {
		t.Errorf("first sync = %+v, want 20 discovered / 20 created / 0 skipped", first)
	}

	second, err := ingestor.Sync(context.Background(), source, fetcher)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if second.ArtifactsDiscovered != 20 || second.EvidenceCreated != 0 || second.EvidenceSkipped != 20 {
		t.Errorf("second sync = %+v, want 20 discovered / 0 created / 20 skipped", second)
	}

	var rows, events int
	if err := db.QueryRow(`SELECT count(*) FROM evidence`).Scan(&rows); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM domain_event WHERE type = ?`, eventEvidenceIngested).Scan(&events); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if rows != 20 {
		t.Errorf("evidence has %d rows after two syncs, want 20", rows)
	}
	if events != 20 {
		t.Errorf("domain_event has %d rows after two syncs, want 20 (D25: one per ingestion)", events)
	}
	if fetcher.fetches != 20 {
		t.Errorf("fetched %d artifacts across two syncs, want 20: the second sync re-downloaded what it already held", fetcher.fetches)
	}
}

// ingestorFor builds an Ingestor with a fixed clock, so the test above
// asserts on counts rather than on time.
func ingestorFor(repo app.EvidenceRepository) *app.Ingestor {
	in := app.NewIngestor(repo)
	in.Now = func() time.Time { return ingestedAt }
	return in
}
