package bbox_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
)

// A day without cargo or abono is furniture, not a movement. The July
// statement prints one such day; the CSV does not.
func TestHSBCLedgerRowsNeedCargoOrAbono(t *testing.T) {
	doc := hsbcDoc(t,
		hsbcWord(43.2, 100, "15"),
		hsbcWord(61.2, 100, "SOLO DIA"),
		hsbcWord(43.2, 120, "16"),
		hsbcWord(61.2, 120, "CGO o"),
		hsbcWord(371.2, 120, "$ 300.00"),
		hsbcWord(532.3, 120, "$ 0.00"),
	)
	rows := bbox.HSBCStatementV1().LedgerRows(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Date != "16" || rows[0].Cargo != "$ 300.00" {
		t.Errorf("row = %+v", rows[0])
	}
}

// The serial wraps about nine points below the reference. It belongs to the
// row above, and it is not a movement of its own.
func TestHSBCReferenceWrapJoinsTheRowAbove(t *testing.T) {
	doc := hsbcDoc(t,
		hsbcWord(43.2, 515.45, "22"),
		hsbcWord(61.2, 515.45, "NETNM DEPOSITO DE NOMINA"),
		hsbcWord(300.4, 515.45, "14594619"),
		hsbcWord(431.0, 515.45, "$ 17,844.58"),
		hsbcWord(521.5, 515.45, "$ 17,844.58"),
		hsbcWord(313.4, 524.75, "41234"),
	)
	rows := bbox.HSBCStatementV1().LedgerRows(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := strings.Join(rows[0].Reference, " / "); got != "14594619 / 41234" {
		t.Errorf("reference = %q", got)
	}
	if rows[0].Abono != "$ 17,844.58" {
		t.Errorf("abono = %q", rows[0].Abono)
	}
	if rows[0].Cargo != "" {
		t.Errorf("cargo = %q, want empty", rows[0].Cargo)
	}
}

// A footer line in the reference column is not a serial and does not join.
func TestHSBCFooterIsNotAReference(t *testing.T) {
	doc := hsbcDoc(t,
		hsbcWord(43.2, 100, "30"),
		hsbcWord(61.2, 100, "CGO o"),
		hsbcWord(300.4, 100, "08045219"),
		hsbcWord(371.2, 100, "$ 300.00"),
		hsbcWord(309.1, 109, "182018"),
		hsbcWord(337.5, 180, "ro HSBC. RFC: HMI-950125KG8"),
	)
	rows := bbox.HSBCStatementV1().LedgerRows(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got := strings.Join(rows[0].Reference, " / "); got != "08045219 / 182018" {
		t.Errorf("reference = %q", got)
	}
}

// Saldo sits in its own column and never becomes the movement amount.
func TestHSBCBalanceIsNotCargoOrAbono(t *testing.T) {
	doc := hsbcDoc(t,
		hsbcWord(43.2, 100, "22"),
		hsbcWord(61.2, 100, "CGO o"),
		hsbcWord(371.2, 100, "$ 300.00"),
		hsbcWord(532.3, 100, "$ 1,000.00"),
	)
	rows := bbox.HSBCStatementV1().LedgerRows(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].Cargo != "$ 300.00" || rows[0].Abono != "" || rows[0].Balance != "$ 1,000.00" {
		t.Errorf("row = cargo %q abono %q balance %q", rows[0].Cargo, rows[0].Abono, rows[0].Balance)
	}
}

func hsbcDoc(t *testing.T, words ...string) *bbox.Document {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	b.WriteString(`<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>`)
	b.WriteString(`<page width="612.000000" height="792.000000"><flow>`)
	for _, w := range words {
		b.WriteString(w)
	}
	b.WriteString(`</flow></page></doc></body></html>`)
	doc, err := bbox.Parse([]byte(b.String()))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return doc
}

func hsbcWord(x, y float64, text string) string {
	return fmt.Sprintf(
		`<block xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<line xMin="%f" yMin="%f" xMax="%f" yMax="%f">`+
			`<word xMin="%f" yMin="%f" xMax="%f" yMax="%f">%s</word>`+
			`</line></block>`,
		x, y, x+40, y+8, x, y, x+40, y+8, x, y, x+40, y+8, text)
}
