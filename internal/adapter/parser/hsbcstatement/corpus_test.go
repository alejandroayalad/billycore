package hsbcstatement_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/alejandroayalad/billycore/internal/adapter/extractor/pdftotext"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/hsbcenc"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/hsbcstatement"
	"github.com/alejandroayalad/billycore/internal/domain"
)

func TestCorpusMatchesTheVersionedCSV(t *testing.T) {
	junePDF, julyPDF, ok := statementPDFs(t)
	if !ok {
		t.Skip("HSBC statement PDFs are not in this checkout")
	}
	want := loadExpected(t)
	gotJune := interpretPDF(t, junePDF)
	gotJuly := interpretPDF(t, julyPDF)

	compareMonth(t, "2026-06", want["2026-06"], gotJune)
	compareMonth(t, "2026-07", want["2026-07"], gotJuly)

	assertReferences(t, "2026-06", junePDF, want["2026-06"])
	assertReferences(t, "2026-07", julyPDF, want["2026-07"])

	assertTrackingKey(t, gotJune, "2026-06-22", 30000, string(domain.Outflow), "HSBC5868712")
	assertTrackingKey(t, gotJune, "2026-06-30", 500000, string(domain.Outflow), "HSBC723535")
	assertNoTrackingKey(t, gotJune, "2026-06-30", 100000, string(domain.Outflow))
}

func assertTrackingKey(t *testing.T, claims []map[domain.FieldName]domain.ClaimField, day string, minor int64, direction, key string) {
	t.Helper()
	zone, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fields := range claims {
		if fields[domain.FieldAmountMinor].Int() != minor || fields[domain.FieldDirection].Text() != direction {
			continue
		}
		at, err := time.Parse(domain.TimeLayout, fields[domain.FieldOccurredAt].Text())
		if err != nil {
			t.Fatal(err)
		}
		if at.In(zone).Format("2006-01-02") != day {
			continue
		}
		found = true
		field, ok := fields[domain.FieldTrackingKey]
		if !ok {
			t.Errorf("%s %d %s has no tracking_key, want %q", day, minor, direction, key)
			continue
		}
		if got := field.Text(); got != key {
			t.Errorf("%s %d %s tracking_key = %q, want %q", day, minor, direction, got, key)
		}
	}
	if !found {
		t.Fatalf("no claim for %s %d %s", day, minor, direction)
	}
}

func assertNoTrackingKey(t *testing.T, claims []map[domain.FieldName]domain.ClaimField, day string, minor int64, direction string) {
	t.Helper()
	zone, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range claims {
		if fields[domain.FieldAmountMinor].Int() != minor || fields[domain.FieldDirection].Text() != direction {
			continue
		}
		at, err := time.Parse(domain.TimeLayout, fields[domain.FieldOccurredAt].Text())
		if err != nil {
			t.Fatal(err)
		}
		if at.In(zone).Format("2006-01-02") != day {
			continue
		}
		if _, ok := fields[domain.FieldTrackingKey]; ok {
			t.Errorf("%s %d %s received a tracking key on a collision", day, minor, direction)
		}
	}
}

func interpretPDF(t *testing.T, pdf []byte) []map[domain.FieldName]domain.ClaimField {
	t.Helper()
	reading, err := hsbcstatement.New(pdftotext.New()).Interpret(t.Context(), pdf)
	if err != nil {
		t.Fatalf("Interpret: %v", err)
	}
	return reading.Fields
}

func compareMonth(t *testing.T, month string, want []expected, got []map[domain.FieldName]domain.ClaimField) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: claims = %d, want %d", month, len(got), len(want))
	}
	zone, err := time.LoadLocation("America/Mexico_City")
	if err != nil {
		t.Fatal(err)
	}
	for i, row := range want {
		fields := got[i]
		at, err := time.Parse(domain.TimeLayout, fields[domain.FieldOccurredAt].Text())
		if err != nil {
			t.Fatalf("%s row %d: occurred_at: %v", month, i, err)
		}
		if gotDay := at.In(zone).Format("2006-01-02"); gotDay != row.date {
			t.Errorf("%s row %d: date = %s, want %s", month, i, gotDay, row.date)
		}
		if fields[domain.FieldAmountMinor].Int() != row.minor {
			t.Errorf("%s row %d: amount = %d, want %d", month, i, fields[domain.FieldAmountMinor].Int(), row.minor)
		}
		if fields[domain.FieldDirection].Text() != row.direction {
			t.Errorf("%s row %d: direction = %s, want %s", month, i, fields[domain.FieldDirection].Text(), row.direction)
		}
	}
}

func assertReferences(t *testing.T, month string, pdf []byte, want []expected) {
	t.Helper()
	coords, err := pdftotext.New().Extract(t.Context(), pdf)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := bbox.Parse(coords)
	if err != nil {
		t.Fatal(err)
	}
	table, err := hsbcenc.FromPDF(pdf)
	if err != nil {
		t.Fatal(err)
	}
	table.DecodeDocument(doc)
	rows := bbox.HSBCStatementV1().LedgerRows(doc)
	if len(rows) != len(want) {
		t.Fatalf("%s: ledger rows = %d, want %d", month, len(rows), len(want))
	}
	for i, row := range want {
		got := strings.Join(rows[i].Reference, " / ")
		if got != row.reference {
			t.Errorf("%s row %d: reference = %q, want %q", month, i, got, row.reference)
		}
		desc := strings.Join(rows[i].Description, " ")
		if fold(desc) != fold(row.description) {
			t.Errorf("%s row %d: description fold %q != %q (got %q, csv %q)",
				month, i, fold(desc), fold(row.description), desc, row.description)
		}
	}
}

type expected struct {
	date        string
	description string
	reference   string
	minor       int64
	direction   string
}

func loadExpected(t *testing.T) map[string][]expected {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "expected_movements.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	records, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]expected{}
	for i, rec := range records {
		if i == 0 {
			continue
		}
		row := expected{
			date:        rec[0],
			description: rec[1],
			reference:   rec[2],
		}
		if rec[3] != "" {
			row.minor = csvMinor(t, rec[3])
			row.direction = string(domain.Outflow)
		} else {
			row.minor = csvMinor(t, rec[4])
			row.direction = string(domain.Inflow)
		}
		out[rec[0][:7]] = append(out[rec[0][:7]], row)
	}
	return out
}

func csvMinor(t *testing.T, s string) int64 {
	t.Helper()
	s = strings.TrimSpace(s)
	if s == "" {
		t.Fatal("empty amount")
	}
	if !strings.Contains(s, ".") {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			t.Fatalf("amount %q: %v", s, err)
		}
		return n * 100
	}
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatalf("amount %q: %v", s, err)
	}
	frac := parts[1]
	if len(frac) == 1 {
		frac += "0"
	}
	if len(frac) != 2 {
		t.Fatalf("amount %q: fraction %q", s, parts[1])
	}
	cents, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		t.Fatalf("amount %q: %v", s, err)
	}
	return whole*100 + cents
}

func fold(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 'e' || r == 'E' || unicode.IsSpace(r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func statementPDFs(t *testing.T) (june, july []byte, ok bool) {
	t.Helper()
	dirs := []string{
		os.Getenv("HSBC_STATEMENT_DIR"),
		"/home/ubuntu/.cursor/projects/workspace/uploads",
		filepath.Join("testdata", "statements"),
		"/workspace/testdata/statements",
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		j6, err6 := findPDF(dir, "2026-06-30")
		j7, err7 := findPDF(dir, "2026-07-31")
		if err6 == nil && err7 == nil {
			return j6, j7, true
		}
	}
	return nil, nil, false
}

func findPDF(dir, prefix string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(strings.ToLower(name), ".pdf") {
			return os.ReadFile(filepath.Join(dir, name))
		}
	}
	return nil, os.ErrNotExist
}
