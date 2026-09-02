package bbox

import (
	"regexp"
	"sort"
	"strings"
)

// HSBCGeometry is the column cut of an HSBC Flex statement. The numbers come
// from the June 2026 and July 2026 pages (D74).
type HSBCGeometry struct {
	DayMinX     float64
	DayMaxX     float64
	DescMinX    float64
	DescMaxX    float64
	RefMinX     float64
	RefMaxX     float64
	CargoMinX   float64
	CargoMaxX   float64
	AbonoMinX   float64
	AbonoMaxX   float64
	BalanceMinX float64
	BalanceMaxX float64
	YTolerance  float64
}

// HSBCStatementV1 is the measured geometry of Cuenta Flexible Simple.
func HSBCStatementV1() HSBCGeometry {
	return HSBCGeometry{
		DayMinX:     40,
		DayMaxX:     58,
		DescMinX:    58,
		DescMaxX:    295,
		RefMinX:     295,
		RefMaxX:     360,
		CargoMinX:   360,
		CargoMaxX:   425,
		AbonoMinX:   425,
		AbonoMaxX:   510,
		BalanceMinX: 510,
		BalanceMaxX: 612,
		YTolerance:  1,
	}
}

var (
	hsbcDay    = regexp.MustCompile(`^\d{1,2}$`)
	hsbcAmount = regexp.MustCompile(`^\$\s*[\d,]+\.\d{2}$`)
	hsbcSerial = regexp.MustCompile(`^\d+$`)
)

type placedWord struct {
	Page int
	Word
}

// LedgerRows returns movement rows. A day is a movement only when cargo or
// abono sits on the same line. A reference that wraps onto the next line joins
// the row above. See D74.
func (g HSBCGeometry) LedgerRows(doc *Document) []Row {
	if doc == nil {
		return nil
	}
	words := placedWords(doc)
	bands := g.bands(words)
	out := make([]Row, 0, len(bands))
	for _, row := range bands {
		if row.Date != "" && (row.Cargo != "" || row.Abono != "") {
			out = append(out, row)
			continue
		}
		if len(out) == 0 {
			continue
		}
		if len(row.Reference) > 0 && row.Date == "" && row.Cargo == "" && row.Abono == "" {
			prev := &out[len(out)-1]
			if prev.Page == row.Page && row.Y-prev.Y <= 12 {
				for _, ref := range row.Reference {
					if hsbcSerial.MatchString(ref) {
						prev.Reference = append(prev.Reference, ref)
					}
				}
			}
		}
	}
	return out
}

func (g HSBCGeometry) bands(words []placedWord) []Row {
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
		case g.in(w.XMin, g.DayMinX, g.DayMaxX) && hsbcDay.MatchString(text):
			row.Date = text
		case g.in(w.XMin, g.DescMinX, g.DescMaxX):
			row.Description = append(row.Description, text)
		case g.in(w.XMin, g.RefMinX, g.RefMaxX) && hsbcSerial.MatchString(text):
			row.Reference = append(row.Reference, text)
		case g.in(w.XMin, g.CargoMinX, g.CargoMaxX) && hsbcAmount.MatchString(text):
			if row.Cargo == "" {
				row.Cargo = text
			}
		case g.in(w.XMin, g.AbonoMinX, g.AbonoMaxX) && hsbcAmount.MatchString(text):
			if row.Abono == "" {
				row.Abono = text
			}
		case g.in(w.XMin, g.BalanceMinX, g.BalanceMaxX) && hsbcAmount.MatchString(strings.TrimSpace(text)):
			if row.Balance == "" {
				row.Balance = strings.TrimSpace(text)
			}
		}
	}
	return out
}

func (g HSBCGeometry) in(x, min, max float64) bool {
	return x >= min && x < max
}

func placedWords(doc *Document) []placedWord {
	var out []placedWord
	for i, page := range doc.Pages {
		for _, b := range page.Blocks {
			for _, l := range b.Lines {
				for _, w := range l.Words {
					out = append(out, placedWord{Page: i + 1, Word: w})
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Page != out[j].Page {
			return out[i].Page < out[j].Page
		}
		if out[i].YMin != out[j].YMin {
			return out[i].YMin < out[j].YMin
		}
		return out[i].XMin < out[j].XMin
	})
	return out
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
