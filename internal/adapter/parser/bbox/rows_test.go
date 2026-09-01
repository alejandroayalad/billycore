package bbox_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
)

// The fixtures are the extractor's, and they describe the documents this
// package reads. A copy here would drift from them. See their README.md.
var fixtureDir = filepath.Join("..", "..", "extractor", "pdftotext", "testdata")

func rowsOf(t *testing.T, fixture string) []bbox.Row {
	t.Helper()
	return bbox.NuStatementV1().Rows(documentOf(t, fixture))
}

func documentOf(t *testing.T, fixture string) *bbox.Document {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, fixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	doc, err := bbox.Parse(data)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return doc
}

// Every fixture pairs each date with an amount on one y, so the count of rows
// is the count of dates. The values prove the pairing found the right amount.
func TestEachFixtureYieldsExactlyItsRows(t *testing.T) {
	cases := map[string][]bbox.Row{
		"nu_2026_05_bbox.xhtml": {
			{Page: 1, Y: 152.93, Date: "31 MAY 2026", Amount: "-$686.69"},
			{Page: 1, Y: 179.02, Date: "31 MAY 2026", Amount: "+$707.71"},
			{Page: 1, Y: 205.11, Date: "31 MAY 2026", Amount: "-$777.78"},
			{Page: 1, Y: 230.20, Date: "31 MAY 2026", Amount: "+$808.81"},
		},
		"nu_2026_06_bbox.xhtml": {
			{Page: 1, Y: 185.12, Date: "19 JUN 2026", Amount: "+$53.54"},
			{Page: 1, Y: 211.21, Date: "18 JUN 2026", Amount: "-$595.60"},
			{Page: 1, Y: 237.30, Date: "18 JUN 2026", Amount: "+$101.11"},
			{Page: 1, Y: 262.39, Date: "18 JUN 2026", Amount: "-$616.62"},
		},
		"nu_2026_07_bbox.xhtml": {
			{Page: 1, Y: 745.72, Date: "31 JUL 2026", Amount: "+$0,606.07"},
			{Page: 2, Y: 184.12, Date: "31 JUL 2026", Amount: "-$1,212.13"},
		},
		"nu_2026_05_fx_bbox.xhtml": {
			{Page: 1, Y: 267.38, Date: "27 MAY 2026", Amount: "-$333.34"},
			{Page: 1, Y: 292.47, Date: "27 MAY 2026", Amount: "-$383.39"},
			{Page: 1, Y: 341.65, Date: "27 MAY 2026", Amount: "+$42.43"},
			{Page: 1, Y: 367.74, Date: "27 MAY 2026", Amount: "-$44.45"},
		},
	}

	for fixture, want := range cases {
		t.Run(fixture, func(t *testing.T) {
			rows := rowsOf(t, fixture)
			if len(rows) != len(want) {
				t.Fatalf("rows = %d, want %d", len(rows), len(want))
			}
			for i, w := range want {
				got := rows[i]
				if got.Page != w.Page || got.Y != w.Y || got.Date != w.Date || got.Amount != w.Amount {
					t.Errorf("row %d = %+v, want page %d y %v %q %q",
						i, got, w.Page, w.Y, w.Date, w.Amount)
				}
			}
		})
	}
}

// The blocks of the May page arrive at 205.1, 204.1, 230.2, 231.7, 205.1,
// 231.7, 230.2. A parser that walks them in sequence pairs the wrong date with
// the wrong amount.
func TestRowsComeOutInPageThenYOrderThoughBlocksDoNot(t *testing.T) {
	for _, fixture := range []string{"nu_2026_05_bbox.xhtml", "nu_2026_07_bbox.xhtml"} {
		t.Run(fixture, func(t *testing.T) {
			doc := documentOf(t, fixture)

			sorted := true
			for _, page := range doc.Pages {
				for i := 1; i < len(page.Blocks); i++ {
					if page.Blocks[i].YMin < page.Blocks[i-1].YMin {
						sorted = false
					}
				}
			}
			if sorted {
				t.Fatal("the fixture arrives in y order; it no longer exercises the hazard")
			}

			rows := bbox.NuStatementV1().Rows(doc)
			for i := 1; i < len(rows); i++ {
				if rows[i].Page < rows[i-1].Page {
					t.Fatalf("row %d is on page %d, after page %d", i, rows[i].Page, rows[i-1].Page)
				}
				if rows[i].Page == rows[i-1].Page && rows[i].Y <= rows[i-1].Y {
					t.Fatalf("row %d at y=%v follows y=%v", i, rows[i].Y, rows[i-1].Y)
				}
			}
		})
	}
}

// The description block sits about one point off the row's y, so an equality
// finds nothing. The last row of the fixture is offset the other way, and one
// row holds two blocks: poppler splits a line on a wide horizontal gap.
func TestTheDescriptionAttachesToItsRowDespiteTheOffset(t *testing.T) {
	want := [][]string{
		{"PANADERIA65 LIBRERIA66 FERRETERIA67 Compra"},
		{"Retiro de Cajita: Mi primera Cajita"},
		{"CUOTA72 RENTA73 Compra"},
		{"VIAJE74 COMIDA75 ALVAREZ76", "delgado79"},
	}

	rows := rowsOf(t, "nu_2026_05_bbox.xhtml")
	if len(rows) != len(want) {
		t.Fatalf("rows = %d, want %d", len(rows), len(want))
	}
	for i, w := range want {
		if strings.Join(rows[i].Description, "|") != strings.Join(w, "|") {
			t.Errorf("row %d description = %q, want %q", i, rows[i].Description, w)
		}
	}
}

// A detail block belongs to the row above it, and every line stays in reading
// order. Two lines of the May detail share a y and are ordered by x.
func TestADetailBlockAttachesToTheRowAboveIt(t *testing.T) {
	rows := rowsOf(t, "nu_2026_05_bbox.xhtml")
	for i, row := range rows[:3] {
		if len(row.Detail) != 0 {
			t.Errorf("row %d holds %d detail lines, want none", i, len(row.Detail))
		}
	}

	detail := rows[3].Detail
	if len(detail) != 6 {
		t.Fatalf("detail lines = %d, want 6", len(detail))
	}
	if !strings.HasPrefix(detail[0], "Depósito SPEI, Hora: 10:22:34,") {
		t.Errorf("detail starts %q", detail[0])
	}
	if detail[1] != "VIAJE74 COMIDA75 ALVAREZ76" || detail[2] != "(Dato no verificado por esta" {
		t.Errorf("the two lines on one y are out of order: %q, %q", detail[1], detail[2])
	}
	if !strings.HasSuffix(detail[5], "referencia 858585") {
		t.Errorf("detail ends %q", detail[5])
	}
}

// The July detail opens page 2 and belongs to the last row of page 1. The
// fixture also opens page 1 with a detail whose row is before this window, and
// that block has no row to hold it.
func TestADetailThatOpensAPageAttachesToTheLastRowOfThePageBefore(t *testing.T) {
	rows := rowsOf(t, "nu_2026_07_bbox.xhtml")
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if len(rows[0].Detail) != 5 {
		t.Fatalf("page 1 row holds %d detail lines, want the 5 that open page 2", len(rows[0].Detail))
	}
	if !strings.HasPrefix(rows[0].Detail[0], "Depósito SPEI, Hora: 12:48:36,") {
		t.Errorf("the carried detail starts %q", rows[0].Detail[0])
	}
	if !strings.HasPrefix(rows[1].Detail[0], "Transferencia SPEI, Hora: 18:54:18,") {
		t.Errorf("the page 2 detail starts %q", rows[1].Detail[0])
	}
	for _, row := range rows {
		for _, line := range row.Detail {
			if strings.Contains(line, "06:42:54") {
				t.Error("the detail that opens the document reached a row below it")
			}
		}
	}

	// The June window opens the same way, and its first row keeps its own
	// detail rather than the one above it.
	june := rowsOf(t, "nu_2026_06_bbox.xhtml")
	for i, row := range june[:3] {
		if len(row.Detail) != 0 {
			t.Errorf("row %d holds %d detail lines, want none", i, len(row.Detail))
		}
	}
	if len(june[3].Detail) != 4 {
		t.Errorf("the last June row holds %d detail lines, want 4", len(june[3].Detail))
	}
}

// The exchange sub-line has a description block and an amount block, and no
// date. It attaches to the purchase above it and it is never a row (D56).
func TestAnExchangeSubLineAttachesToItsPurchaseAndIsNotARow(t *testing.T) {
	rows := rowsOf(t, "nu_2026_05_fx_bbox.xhtml")
	if len(rows) != 4 {
		t.Fatalf("rows = %d, want 4", len(rows))
	}

	want := []string{"USD 1.00 = MXN 17.404", "USD 20"}
	if strings.Join(rows[1].Exchange, "|") != strings.Join(want, "|") {
		t.Errorf("exchange = %q, want %q", rows[1].Exchange, want)
	}
	if rows[1].Amount != "-$383.39" {
		t.Errorf("the purchase amount = %q, want the pesos Nu settled", rows[1].Amount)
	}
	for i, row := range rows {
		if i != 1 && len(row.Exchange) != 0 {
			t.Errorf("row %d holds an exchange sub-line", i)
		}
		if len(row.Detail) != 0 {
			t.Errorf("row %d holds a detail; the sub-line is not a detail", i)
		}
	}
}

// The furniture of a page is in the columns a row uses. None of it is a row,
// and none of it reaches one.
func TestPageFurnitureProducesNoRow(t *testing.T) {
	furniture := []string{
		"FECHA", "DEL 01 AL", "MONTO EN PESOS MEXICANOS",
		"Cuenta Nu:", "Nubank", "de 17", "Detalle de movimientos",
	}

	for _, fixture := range []string{
		"nu_2026_05_bbox.xhtml", "nu_2026_06_bbox.xhtml",
		"nu_2026_07_bbox.xhtml", "nu_2026_05_fx_bbox.xhtml",
	} {
		t.Run(fixture, func(t *testing.T) {
			for i, row := range rowsOf(t, fixture) {
				lines := append([]string{row.Date, row.Amount}, row.Description...)
				lines = append(lines, row.Detail...)
				lines = append(lines, row.Exchange...)
				for _, line := range lines {
					for _, word := range furniture {
						if strings.Contains(line, word) {
							t.Errorf("row %d holds the page furniture %q in %q", i, word, line)
						}
					}
				}
			}
		})
	}
}

// An unsigned figure in the amount column is a summary total. Three statements
// hold 18 of them, and not one is a movement.
func TestAnUnsignedAmountProducesNoRow(t *testing.T) {
	for _, amount := range []string{"$35.36", "$22,222.23"} {
		if rows := bbox.NuStatementV1().Rows(oneMovement(t, amount)); len(rows) != 0 {
			t.Errorf("amount %q produced %d rows, want none", amount, len(rows))
		}
	}
	rows := bbox.NuStatementV1().Rows(oneMovement(t, "-$35.36"))
	if len(rows) != 1 {
		t.Fatalf("a signed amount produced %d rows, want 1", len(rows))
	}
}

// oneMovement builds a page holding one date and one figure on its y.
func oneMovement(t *testing.T, amount string) *bbox.Document {
	t.Helper()
	const template = `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>
<page width="595.000000" height="842.000000"><flow>
<block xMin="56.000000" yMin="152.930000" xMax="116.690000" yMax="162.930000">
<line xMin="56.000000" yMin="152.930000" xMax="116.690000" yMax="162.930000">
<word xMin="56.000000" yMin="152.930000" xMax="66.030000" yMax="162.930000">31</word>
<word xMin="68.680000" yMin="152.930000" xMax="89.960000" yMax="162.930000">MAY</word>
<word xMin="92.610000" yMin="152.930000" xMax="116.690000" yMax="162.930000">2026</word>
</line></block>
<block xMin="494.640000" yMin="152.930000" xMax="539.000000" yMax="162.930000">
<line xMin="494.640000" yMin="152.930000" xMax="539.000000" yMax="162.930000">
<word xMin="494.640000" yMin="152.930000" xMax="539.000000" yMax="162.930000">%s</word>
</line></block>
</flow></page></doc></body></html>`

	doc, err := bbox.Parse([]byte(fmt.Sprintf(template, amount)))
	if err != nil {
		t.Fatalf("parse document: %v", err)
	}
	return doc
}
