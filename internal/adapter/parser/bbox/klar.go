package bbox

import (
	"regexp"
	"strings"
)

// KlarGeometry is the column cut of a Klar Cuenta a la Vista statement.
// The numbers come from the June 2026 and July 2026 pages (D77).
type KlarGeometry struct {
	DateMinX    float64
	DateMaxX    float64
	DescMinX    float64
	DescMaxX    float64
	CargoMinX   float64
	CargoMaxX   float64
	AbonoMinX   float64
	AbonoMaxX   float64
	BalanceMinX float64
	BalanceMaxX float64
	YTolerance  float64
	WrapY       float64
}

// KlarStatementV1 is the measured geometry of Klar Principal.
func KlarStatementV1() KlarGeometry {
	return KlarGeometry{
		DateMinX:    40,
		DateMaxX:    100,
		DescMinX:    100,
		DescMaxX:    330,
		CargoMinX:   330,
		CargoMaxX:   395,
		AbonoMinX:   395,
		AbonoMaxX:   480,
		BalanceMinX: 480,
		BalanceMaxX: 580,
		YTolerance:  2,
		WrapY:       16,
	}
}

var (
	klarDate   = regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`)
	klarAmount = regexp.MustCompile(`^\$[\d,]+\.\d{2}$`)
)

func (g KlarGeometry) in(x, min, max float64) bool {
	return x >= min && x < max
}

func klarMoney(s string) bool {
	return s != "" && s != "$0.00" && klarAmount.MatchString(s)
}

// LedgerRows returns the Principal movements. A date is a movement only when
// cargo or abono is a non-zero amount. A wrapped concept line joins the dated
// row above or below it. Interest and investment tables use other columns and
// are not movements here (D59, D77).
func (g KlarGeometry) LedgerRows(doc *Document) []Row {
	if doc == nil {
		return nil
	}
	bands := g.bands(placedWords(doc))
	out := make([]Row, 0, len(bands))
	for _, row := range bands {
		if klarDate.MatchString(row.Date) && (klarMoney(row.Cargo) || klarMoney(row.Abono)) {
			out = append(out, row)
		}
	}
	for _, extra := range bands {
		if len(extra.Description) == 0 {
			continue
		}
		if klarDate.MatchString(extra.Date) && (klarMoney(extra.Cargo) || klarMoney(extra.Abono)) {
			continue
		}
		g.joinWrap(out, extra)
	}
	return out
}

func (g KlarGeometry) joinWrap(rows []Row, extra Row) {
	best := -1
	bestDy := g.WrapY
	for i := range rows {
		if rows[i].Page != extra.Page {
			continue
		}
		dy := abs(rows[i].Y - extra.Y)
		if dy <= bestDy {
			bestDy = dy
			best = i
		}
	}
	if best < 0 {
		return
	}
	rows[best].Description = append(rows[best].Description, extra.Description...)
}

func (g KlarGeometry) bands(words []placedWord) []Row {
	var out []Row
	for _, w := range words {
		text := strings.TrimSpace(w.Text)
		if text == "" {
			continue
		}
		var row *Row
		if n := len(out); n > 0 && out[n-1].Page == w.Page && abs(out[n-1].Y-w.YMin) <= g.YTolerance {
			row = &out[n-1]
		} else {
			out = append(out, Row{Page: w.Page, Y: w.YMin})
			row = &out[len(out)-1]
		}
		switch {
		case g.in(w.XMin, g.DateMinX, g.DateMaxX) && klarDate.MatchString(text):
			row.Date = text
		case g.in(w.XMin, g.DescMinX, g.DescMaxX):
			row.Description = append(row.Description, text)
		case g.in(w.XMin, g.CargoMinX, g.CargoMaxX) && klarAmount.MatchString(text):
			if row.Cargo == "" {
				row.Cargo = text
			}
		case g.in(w.XMin, g.AbonoMinX, g.AbonoMaxX) && klarAmount.MatchString(text):
			if row.Abono == "" {
				row.Abono = text
			}
		case g.in(w.XMin, g.BalanceMinX, g.BalanceMaxX) && klarAmount.MatchString(text):
			if row.Balance == "" {
				row.Balance = text
			}
		}
	}
	return out
}
