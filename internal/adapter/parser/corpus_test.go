package parser_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"

	_ "modernc.org/sqlite"
)

// The corpus test runs the parsers over every artifact in the real database.
//
// It is a fence, not a fixture: 1,044 artifacts reaching back to 2023 are not
// a thing that can be re-collected on demand, and a parser that is quietly
// wrong on one template in fifty is exactly what unit tests over five hand
// picked fixtures will not catch. It asserts aggregate invariants — every
// amount positive and carrying a currency, every date inside its own
// artifact's plausible range — rather than specific values, because the values
// are the user's and do not belong in a public repository.
//
// It opens the database read-only. Evidence is immutable (D7, D10) and the
// parser layer has no business writing to it under any circumstance, test
// included.
//
// It skips cleanly where the database is absent, which is every machine that
// is not the author's.
func TestCorpus(t *testing.T) {
	path := corpusPath(t)
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT source_reference, raw_content, observed_at FROM evidence ORDER BY observed_at`)
	if err != nil {
		t.Fatalf("query corpus: %v", err)
	}
	defer rows.Close()

	var stats corpusStats
	for rows.Next() {
		var ref, observedAt string
		var raw []byte
		if err := rows.Scan(&ref, &raw, &observedAt); err != nil {
			t.Fatalf("scan: %v", err)
		}
		checkArtifact(t, &stats, ref, raw, observedAt)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate corpus: %v", err)
	}

	t.Logf("artifacts %d · transaction-bearing %d · amounts %d · dates %d · clocks %d",
		stats.artifacts, stats.txBearing, stats.amounts, stats.dates, stats.clocks)
	for _, name := range templateOrder {
		t.Logf("  %-32s %4d", name, stats.perTemplate[name])
	}

	// Measured 2026-08-25. These are counts, not estimates, and a change in
	// one means either a new sync or a regression — both worth stopping for.
	assertCount(t, "artifacts", stats.artifacts, 1044)
	assertCount(t, "transaction-bearing", stats.txBearing, 800)

	// Amount is the field that is never missing: it was present in 800 of 800.
	assertCount(t, "amounts parsed", stats.amounts, stats.txBearing)

	// The two transfer templates carry a labelled `Fecha:` and `Hora:`; the
	// other two do not. The card payment receipt has no body date at all —
	// DATA_MODEL.md §4.5 already anticipated that by falling back to the
	// delivery timestamp — and the service receipt writes its timestamp as
	// unlabelled prose, which is the template parser's problem, not this
	// layer's.
	assertCount(t, "dates parsed", stats.dates, 699)
	assertCount(t, "clocks parsed", stats.clocks, 699)

	for name, want := range map[string]int{
		"Tu transferencia fue exitosa":       354,
		"¡Recibiste una transferencia!":      345,
		"¡Recibimos tu pago!":                90,
		"Tu comprobante de pago de servicio": 11,
	} {
		assertCount(t, name, stats.perTemplate[name], want)
	}
}

type corpusStats struct {
	artifacts, txBearing, amounts, dates, clocks int
	perTemplate                                  map[string]int
}

// templates maps a subject to the label its amount sits behind. The card
// payment receipt has no labels at all, so its amount is the first one in the
// body — which is template knowledge, and lives here in the test rather than
// in the package, because template parsers are the next step and not this one.
var templates = map[string]string{
	"Tu transferencia fue exitosa":       "Monto:",
	"¡Recibiste una transferencia!":      "Monto:",
	"Tu comprobante de pago de servicio": "Monto:",
	"¡Recibimos tu pago!":                "",
}

var templateOrder = []string{
	"Tu transferencia fue exitosa",
	"¡Recibiste una transferencia!",
	"¡Recibimos tu pago!",
	"Tu comprobante de pago de servicio",
}

func checkArtifact(t *testing.T, stats *corpusStats, ref string, raw []byte, observedAt string) {
	t.Helper()
	if stats.perTemplate == nil {
		stats.perTemplate = map[string]int{}
	}
	stats.artifacts++

	body, err := parser.HTMLBody(raw)
	if err != nil {
		t.Errorf("%s: HTMLBody: %v", ref, err)
		return
	}
	text, err := parser.Text(body)
	if err != nil {
		t.Errorf("%s: Text: %v", ref, err)
		return
	}
	// A URL is deliberately not a leak: 37 artifacts print one as visible
	// text in a regulatory footer. What must never appear is stylesheet
	// content, which is markup machinery and would put "$0.00" and "13px" in
	// front of a value parser.
	for _, css := range []string{"!important", "font-size:", "mso-"} {
		if strings.Contains(text, css) {
			t.Errorf("%s: CSS leaked into the extracted text (%q)", ref, css)
			break
		}
	}

	subject, err := parser.Header(raw, "Subject")
	if err != nil {
		t.Errorf("%s: Header: %v", ref, err)
		return
	}
	label, isTx := templates[subject]
	if !isTx {
		return
	}
	stats.txBearing++
	stats.perTemplate[subject]++

	observed, err := time.Parse(time.RFC3339, observedAt)
	if err != nil {
		t.Errorf("%s: observed_at %q: %v", ref, observedAt, err)
		return
	}

	// Amount.
	amountText := valueAfter(text, label)
	if label == "" {
		amountText = firstAmount(text)
	}
	money, err := parser.ParseMoney(amountText, "MXN")
	switch {
	case err != nil:
		t.Errorf("%s (%s): ParseMoney(%q): %v", ref, subject, amountText, err)
	case money.Minor() <= 0:
		t.Errorf("%s (%s): amount is %d minor units; a transaction of nothing is not one", ref, subject, money.Minor())
	case money.Currency() != "MXN":
		t.Errorf("%s (%s): amount lost its currency", ref, subject)
	default:
		stats.amounts++
	}

	// Date, where the template has one.
	if raw := valueAfter(text, "Fecha:"); raw != "" {
		wall, err := parser.ParseWall(raw)
		if err != nil {
			t.Errorf("%s (%s): ParseWall(%q): %v", ref, subject, raw, err)
		} else {
			// The plausible range is the artifact's own delivery timestamp.
			// Two days absorbs the unstated zone and any delivery lag, and
			// still catches a day/month transposition or a bad century.
			if d := wall.In(time.UTC).Sub(observed); d > 48*time.Hour || d < -48*time.Hour {
				t.Errorf("%s (%s): body date %s is %v from the delivery timestamp %s",
					ref, subject, wall, d, observed.Format(time.RFC3339))
			}
			stats.dates++
		}
	}

	if raw := valueAfter(text, "Hora:"); raw != "" {
		if _, _, _, err := parser.ParseClock(raw); err != nil {
			t.Errorf("%s (%s): ParseClock(%q): %v", ref, subject, raw, err)
		} else {
			stats.clocks++
		}
	}
}

// valueAfter returns the text following a label, on the same line where the
// template puts it there and on the next non-empty line where it does not.
// Line *index* is never used: only 35 of 345 inflows share a line structure.
func valueAfter(text, label string) string {
	if label == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		at := strings.Index(line, label)
		if at < 0 {
			continue
		}
		if rest := strings.TrimSpace(line[at+len(label):]); rest != "" {
			return rest
		}
		if i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	return ""
}

// firstAmount finds the leading `$` token, for the one template with no labels.
func firstAmount(text string) string {
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "$") {
			return field
		}
	}
	return ""
}

func assertCount(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d (measured 2026-08-25)", what, got, want)
	}
}

func corpusPath(t *testing.T) string {
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
		if errors.Is(err, os.ErrNotExist) {
			t.Skipf("no corpus at %s; set BILLYCORE_CORPUS_DB to run this test", path)
		}
		t.Skipf("cannot reach the corpus at %s: %v", path, err)
	}
	return path
}
