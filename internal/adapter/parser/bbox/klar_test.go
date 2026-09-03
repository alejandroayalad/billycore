package bbox_test

import (
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
)

func TestKlarLedgerRowsNeedCargoOrAbono(t *testing.T) {
	doc := hsbcDoc(t,
		klarWord(45, 100, "02/06/2026"),
		klarWord(104, 100, "als"),
		klarWord(346, 100, "$14.00"),
		klarWord(506, 100, "$0.37"),
		klarWord(45, 140, "08/06/2026"),
		klarWord(104, 140, "Payment"),
		klarWord(144, 140, "to"),
		klarWord(154, 140, "Customers"),
		klarWord(410, 140, "$1,100.00"),
		klarWord(499, 140, "$1,100.37"),
	)
	rows := bbox.KlarStatementV1().LedgerRows(doc)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if rows[0].Date != "02/06/2026" || rows[0].Cargo != "$14.00" || rows[0].Abono != "" {
		t.Errorf("cargo row = %+v", rows[0])
	}
	if rows[1].Date != "08/06/2026" || rows[1].Abono != "$1,100.00" || rows[1].Cargo != "" {
		t.Errorf("abono row = %+v", rows[1])
	}
}

func TestKlarWrappedConceptJoinsTheDatedRow(t *testing.T) {
	doc := hsbcDoc(t,
		klarWord(104, 414.99, "Farmacia"),
		klarWord(145, 414.99, "Bazar"),
		klarWord(45, 422.99, "13/06/2026"),
		klarWord(345, 422.99, "$33.00"),
		klarWord(502, 422.99, "$186.37"),
		klarWord(104, 430.99, "************2099"),
	)
	rows := bbox.KlarStatementV1().LedgerRows(doc)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := strings.Join(rows[0].Description, " ")
	if !strings.Contains(got, "Farmacia") || !strings.Contains(got, "************2099") {
		t.Errorf("description = %q, want the wrap and the mask", got)
	}
	if rows[0].Cargo != "$33.00" {
		t.Errorf("cargo = %q", rows[0].Cargo)
	}
}

func TestKlarZeroAbonoIsNotAMovement(t *testing.T) {
	doc := hsbcDoc(t,
		klarWord(45, 583, "01/06/2026"),
		klarWord(104, 583, "Pago"),
		klarWord(178, 583, "intereses"),
		klarWord(417, 583, "$0.00"),
		klarWord(506, 583, "$0.07"),
	)
	rows := bbox.KlarStatementV1().LedgerRows(doc)
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none: interest is not Principal", rows)
	}
}

func klarWord(x, y float64, text string) string {
	return hsbcWord(x, y, text)
}
