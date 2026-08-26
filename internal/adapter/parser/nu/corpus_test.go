package nu_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"

	_ "modernc.org/sqlite"
)

// TestCorpus runs the four template parsers over every stored artifact.
//
// Same contract as the corpus test one package up: read-only, aggregate
// invariants rather than values, and a clean skip where the database is absent.
func TestCorpus(t *testing.T) {
	db := openCorpus(t)
	defer db.Close()

	rows, err := db.Query(`SELECT source_reference, raw_content, observed_at FROM evidence ORDER BY observed_at`)
	if err != nil {
		t.Fatalf("query corpus: %v", err)
	}
	defer rows.Close()

	var (
		artifacts, recognised, unrecognised int
		perTemplate                         = map[parser.Template]int{}
		withDirection, withDate             int
		drift                               []time.Duration
	)

	for rows.Next() {
		var ref, observedAt string
		var raw []byte
		if err := rows.Scan(&ref, &raw, &observedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		artifacts++

		got, err := nu.Parse(raw)
		if errors.Is(err, parser.ErrNoTemplate) {
			unrecognised++
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", ref, err)
			continue
		}
		recognised++
		perTemplate[got.Template]++

		// Amount: always present, always positive, always with a currency.
		if got.Amount.Minor() <= 0 {
			t.Errorf("%s (%s): amount is %d minor units", ref, got.Template, got.Amount.Minor())
		}
		if got.Amount.Currency() != nu.Currency {
			t.Errorf("%s (%s): amount currency is %q", ref, got.Template, got.Amount.Currency())
		}

		// Direction, where the template settles it, is a valid domain value.
		if got.Direction != "" {
			if err := got.Direction.Validate(); err != nil {
				t.Errorf("%s (%s): %v", ref, got.Template, err)
			}
			withDirection++
		} else if got.Template != nu.TemplateCardPayment {
			t.Errorf("%s (%s): no direction, and only the card payment template may omit one", ref, got.Template)
		}

		// A counterparty is never blank on a template that carries one.
		if got.Counterparty == "" {
			t.Errorf("%s (%s): counterparty is empty", ref, got.Template)
		}

		if got.OccurredAt.IsZero() {
			if got.Template != nu.TemplateCardPayment {
				t.Errorf("%s (%s): no timestamp, and only the card payment template lacks one", ref, got.Template)
			}
			continue
		}
		withDate++

		observed, err := time.Parse(time.RFC3339, observedAt)
		if err != nil {
			t.Errorf("%s: observed_at %q: %v", ref, observedAt, err)
			continue
		}
		drift = append(drift, got.OccurredAt.Sub(observed))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate corpus: %v", err)
	}

	t.Logf("artifacts %d · recognised %d · unrecognised %d · with a direction %d · with a body timestamp %d",
		artifacts, recognised, unrecognised, withDirection, withDate)
	for _, tpl := range []parser.Template{
		nu.TemplateTransferOut, nu.TemplateTransferIn, nu.TemplateCardPayment, nu.TemplateServicePayment,
	} {
		t.Logf("  %-22s %4d", tpl, perTemplate[tpl])
	}
	reportDrift(t, drift)

	// Measured 2026-08-25.
	assertCount(t, "artifacts", artifacts, 1044)
	assertCount(t, "recognised", recognised, 800)
	assertCount(t, "unrecognised", unrecognised, 244)
	assertCount(t, "NU_TRANSFER_OUT", perTemplate[nu.TemplateTransferOut], 354)
	assertCount(t, "NU_TRANSFER_IN", perTemplate[nu.TemplateTransferIn], 345)
	assertCount(t, "NU_CARD_PAYMENT", perTemplate[nu.TemplateCardPayment], 90)
	assertCount(t, "NU_SERVICE_PAYMENT", perTemplate[nu.TemplateServicePayment], 11)
	assertCount(t, "with a body timestamp", withDate, 710)
	assertCount(t, "with a direction", withDirection, 710)
}

// reportDrift compares each parsed timestamp against Gmail's own delivery
// timestamp for the same artifact.
//
// This is the test that makes D29 falsifiable. The two numbers are independent:
// one is Nu's wall clock inside the body, read as America/Mexico_City; the
// other is Gmail's `internalDate`, recorded in UTC by a different system
// entirely. If the zone were wrong they would disagree by whole hours. That
// they agree to within minutes is evidence for the decision rather than a
// restatement of it.
func reportDrift(t *testing.T, drift []time.Duration) {
	t.Helper()
	if len(drift) == 0 {
		return
	}
	sort.Slice(drift, func(i, j int) bool { return drift[i] < drift[j] })
	t.Logf("body timestamp minus delivery timestamp: min %v · median %v · max %v",
		drift[0], drift[len(drift)/2], drift[len(drift)-1])

	// An hour absorbs the seconds of delivery latency and any minute Nu rounds
	// off, and is far tighter than the six hours a wrong zone would cost.
	for _, d := range []time.Duration{drift[0], drift[len(drift)-1]} {
		if d > time.Hour || d < -time.Hour {
			t.Errorf("a parsed timestamp is %v from its delivery timestamp; D29's zone looks wrong", d)
		}
	}
}

func assertCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d (measured 2026-08-25)", what, got, want)
	}
}

func openCorpus(t *testing.T) *sql.DB {
	t.Helper()
	path := os.Getenv("BILLYCORE_CORPUS_DB")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("no home directory, so no corpus: %v", err)
		}
		path = filepath.Join(home, ".billy", "billy.db")
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("no corpus at %s; set BILLYCORE_CORPUS_DB to run this test", path)
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	return db
}
