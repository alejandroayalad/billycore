package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// No database and no network. The use case is exercised entirely through its
// ports, which is the property D6 exists to buy.

var (
	syncedAt   = time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	receivedAt = time.Date(2026, 8, 16, 17, 44, 0, 0, time.UTC)

	gmailPrimary = Source{ID: "gmail_primary", Type: domain.SourceGmail}
)

// fakeRepo records what it was asked to store, in order.
type fakeRepo struct {
	stored     []domain.Evidence
	insertedAt []time.Time
	existsErr  error
	insertErr  error
}

func (f *fakeRepo) Insert(_ context.Context, e domain.Evidence, now time.Time) (bool, error) {
	if f.insertErr != nil {
		return false, f.insertErr
	}
	for _, existing := range f.stored {
		if existing.SourceID() == e.SourceID() && existing.SourceReference() == e.SourceReference() {
			return false, nil // ON CONFLICT DO NOTHING
		}
	}
	f.stored = append(f.stored, e)
	f.insertedAt = append(f.insertedAt, now)
	return true, nil
}

func (f *fakeRepo) GetByID(_ context.Context, id string) (domain.Evidence, error) {
	for _, e := range f.stored {
		if e.ID() == id {
			return e, nil
		}
	}
	return domain.Evidence{}, errors.New("not found")
}

func (f *fakeRepo) ExistsByReference(_ context.Context, sourceID, sourceReference string) (bool, error) {
	if f.existsErr != nil {
		return false, f.existsErr
	}
	for _, e := range f.stored {
		if e.SourceID() == sourceID && e.SourceReference() == sourceReference {
			return true, nil
		}
	}
	return false, nil
}

// fakeFetcher is a Source that never touches the network, and counts what was
// asked of it.
type fakeFetcher struct {
	references []string
	content    map[string][]byte
	fetched    []string
	listErr    error
	fetchErr   map[string]error
}

func (f *fakeFetcher) ListReferences(context.Context) ([]string, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.references, nil
}

func (f *fakeFetcher) Fetch(_ context.Context, reference string) (Artifact, error) {
	f.fetched = append(f.fetched, reference)
	if err, bad := f.fetchErr[reference]; bad {
		return Artifact{}, err
	}
	return Artifact{
		Reference:   reference,
		ContentType: "message/rfc822",
		ObservedAt:  receivedAt,
		Content:     f.content[reference],
	}, nil
}

func newFetcher(references ...string) *fakeFetcher {
	f := &fakeFetcher{references: references, content: map[string][]byte{}, fetchErr: map[string]error{}}
	for _, r := range references {
		f.content[r] = []byte("From: nu@nu.com.mx\r\nSubject: Tu transferencia fue exitosa\r\n\r\n" + r)
	}
	return f
}

// ids hands out predictable identifiers so a test can name what was stored.
func ids(prefix string) func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return prefix + "-" + string(rune('0'+n)), nil
	}
}

func newTestIngestor(repo EvidenceRepository) *Ingestor {
	return &Ingestor{
		Repo:  repo,
		NewID: ids("evidence"),
		Now:   func() time.Time { return syncedAt },
	}
}

func TestSyncRecordsEveryArtifactOnce(t *testing.T) {
	repo := &fakeRepo{}
	fetcher := newFetcher("msg-a", "msg-b", "msg-c")

	got, err := newTestIngestor(repo).Sync(context.Background(), gmailPrimary, fetcher)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	want := SyncResult{
		SourceID:            "gmail_primary",
		ArtifactsDiscovered: 3,
		EvidenceCreated:     3,
		EvidenceSkipped:     0,
		CompletedAt:         syncedAt,
	}
	if got != want {
		t.Errorf("Sync = %+v, want %+v", got, want)
	}
	if len(repo.stored) != 3 {
		t.Fatalf("stored %d artifacts, want 3", len(repo.stored))
	}

	e := repo.stored[0]
	if e.SourceID() != "gmail_primary" || e.SourceType() != domain.SourceGmail {
		t.Errorf("Evidence carries the wrong Source: %s / %s", e.SourceID(), e.SourceType())
	}
	if e.SourceReference() != "msg-a" {
		t.Errorf("source_reference = %q, want the artifact reference", e.SourceReference())
	}
	if e.ContentType() != "message/rfc822" {
		t.Errorf("content_type = %q, want message/rfc822", e.ContentType())
	}
	if !e.ObservedAt().Equal(receivedAt) {
		t.Errorf("observed_at = %s, want the Source's receive time %s", e.ObservedAt(), receivedAt)
	}
	if string(e.RawContent()) != string(fetcher.content["msg-a"]) {
		t.Error("the artifact was not stored verbatim")
	}
}

// The second run of M1's acceptance: everything discovered, nothing created.
func TestSyncIsIdempotent(t *testing.T) {
	repo := &fakeRepo{}
	fetcher := newFetcher("msg-a", "msg-b", "msg-c")
	ingestor := newTestIngestor(repo)

	if _, err := ingestor.Sync(context.Background(), gmailPrimary, fetcher); err != nil {
		t.Fatalf("first Sync: %v", err)
	}

	ingestor.NewID = ids("second")
	got, err := ingestor.Sync(context.Background(), gmailPrimary, fetcher)
	if err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if got.ArtifactsDiscovered != 3 || got.EvidenceCreated != 0 || got.EvidenceSkipped != 3 {
		t.Errorf("second Sync = %+v, want 3 discovered / 0 created / 3 skipped", got)
	}
	if len(repo.stored) != 3 {
		t.Errorf("stored %d artifacts after two syncs, want 3", len(repo.stored))
	}
}

// An artifact already recorded is not downloaded again. Evidence is immutable,
// so there is nothing a re-fetch could teach it.
func TestSyncDoesNotRefetchRecordedArtifacts(t *testing.T) {
	repo := &fakeRepo{}
	fetcher := newFetcher("msg-a", "msg-b")
	ingestor := newTestIngestor(repo)

	if _, err := ingestor.Sync(context.Background(), gmailPrimary, fetcher); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	fetcher.fetched = nil

	if _, err := ingestor.Sync(context.Background(), gmailPrimary, fetcher); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(fetcher.fetched) != 0 {
		t.Errorf("re-fetched %v on a sync that created nothing", fetcher.fetched)
	}
}

// Evidence is recorded before anything interprets it, and ingestion stops
// there (D7). Nothing in this layer may produce a Claim.
func TestSyncRecordsTheArtifactVerbatim(t *testing.T) {
	repo := &fakeRepo{}
	fetcher := newFetcher("msg-a")
	fetcher.content["msg-a"] = []byte("Monto: $1,000.00\r\nFolio: QURN4HRT7")

	if _, err := newTestIngestor(repo).Sync(context.Background(), gmailPrimary, fetcher); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := string(repo.stored[0].RawContent()); got != "Monto: $1,000.00\r\nFolio: QURN4HRT7" {
		t.Errorf("raw content = %q: ingestion altered the artifact", got)
	}
}

func TestSyncFailsWhenAnArtifactCannotBeFetched(t *testing.T) {
	repo := &fakeRepo{}
	fetcher := newFetcher("msg-a", "msg-b", "msg-c")
	fetcher.fetchErr["msg-b"] = errors.New("gmail API returned 500 Internal Server Error")

	_, err := newTestIngestor(repo).Sync(context.Background(), gmailPrimary, fetcher)
	if err == nil {
		t.Fatal("Sync reported success after an artifact could not be fetched")
	}
	if !strings.Contains(err.Error(), "msg-b") {
		t.Errorf("error does not name the artifact that failed: %v", err)
	}
	// What was recorded before the failure stays recorded — the next run resumes
	// from there, because ingestion is idempotent.
	if len(repo.stored) != 1 {
		t.Errorf("stored %d artifacts, want the 1 recorded before the failure", len(repo.stored))
	}
}

func TestSyncFailsWhenTheSourceCannotBeListed(t *testing.T) {
	fetcher := newFetcher()
	fetcher.listErr = errors.New("gmail API returned 401 Unauthorized")

	_, err := newTestIngestor(&fakeRepo{}).Sync(context.Background(), gmailPrimary, fetcher)
	if err == nil {
		t.Fatal("Sync reported success when the Source could not be listed")
	}
}

func TestSyncStopsWhenTheContextIsCancelled(t *testing.T) {
	repo := &fakeRepo{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newTestIngestor(repo).Sync(ctx, gmailPrimary, newFetcher("msg-a", "msg-b"))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(repo.stored) != 0 {
		t.Errorf("stored %d artifacts after cancellation", len(repo.stored))
	}
}

// An artifact the domain rejects fails the sync loudly rather than being
// recorded as something Evidence's invariants do not allow.
func TestSyncRejectsAnArtifactWithNoObservedTime(t *testing.T) {
	fetcher := newFetcher("msg-a")
	fetcher.content["msg-a"] = []byte("body")
	broken := &brokenTimeFetcher{fakeFetcher: fetcher}

	_, err := newTestIngestor(&fakeRepo{}).Sync(context.Background(), gmailPrimary, broken)
	if err == nil {
		t.Fatal("Sync accepted an artifact with no observed time")
	}
	if !errors.Is(err, domain.ErrEvidenceNoObservedAt) {
		t.Errorf("err = %v, want the domain's own rejection", err)
	}
}

type brokenTimeFetcher struct{ *fakeFetcher }

func (b *brokenTimeFetcher) Fetch(ctx context.Context, reference string) (Artifact, error) {
	a, err := b.fakeFetcher.Fetch(ctx, reference)
	a.ObservedAt = time.Time{}
	return a, err
}
