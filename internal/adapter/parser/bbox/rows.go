package bbox

import (
	"math"
	"regexp"
	"sort"
)

// Row is one movement, as the coordinates give it. Every field is text. The
// statement parser above reads the money and the date (D54). Cargo, Abono,
// Balance and Reference are empty on the Nu geometry.
type Row struct {
	Page        int
	Y           float64
	Date        string
	Amount      string
	Description []string
	Detail      []string
	Exchange    []string
	Reference   []string
	Cargo       string
	Abono       string
	Balance     string
}

// Geometry states where one layout puts its columns. It is a value and not a
// set of constants, because D52 expects HSBC_STATEMENT_V1 to measure
// differently. The numbers come from the May, June and July 2026 statements.
type Geometry struct {
	DateColumnMaxX    float64
	DescriptionMinX   float64
	DescriptionLeftX  float64
	AmountColumnMinX  float64
	YTolerance        float64
	LeftEdgeTolerance float64
	Date              *regexp.Regexp
	Amount            *regexp.Regexp
}

// NuStatementV1 is the geometry of the Nu statement, measured on 548 rows.
func NuStatementV1() Geometry {
	return Geometry{
		DateColumnMaxX:    130,
		DescriptionMinX:   130,
		DescriptionLeftX:  135.84,
		AmountColumnMinX:  480,
		YTolerance:        2,
		LeftEdgeTolerance: 1,
		Date:              regexp.MustCompile(`^\d{1,2} [A-Z]{3} \d{4}$`),
		Amount:            regexp.MustCompile(`^[+-]\$[\d,]+\.\d{2}$`),
	}
}

// IsDate reports a block of the date column that holds one date.
func (g Geometry) IsDate(b Block) bool {
	return b.XMin < g.DateColumnMaxX && len(b.Lines) == 1 && g.Date.MatchString(b.Lines[0].Text())
}

// IsAmount reports a block of the amount column that holds one signed figure.
// An unsigned figure is a summary total and never a movement.
func (g Geometry) IsAmount(b Block) bool {
	return b.XMin > g.AmountColumnMinX && len(b.Lines) == 1 && g.Amount.MatchString(b.Lines[0].Text())
}

// IsDescription reports a block of the description column.
func (g Geometry) IsDescription(b Block) bool {
	return b.XMin > g.DescriptionMinX && b.XMin < g.AmountColumnMinX
}

// Rows assembles the movements of a document, in page then y order.
//
// A date and its amount share one y, and that pair is the row (D54). A
// description block below a row is its detail. A description block with an
// amount block beside it and no date is an exchange sub-line, and it is never
// a row (D56).
func (g Geometry) Rows(doc *Document) []Row {
	if doc == nil {
		return nil
	}
	var rows []Row
	for i, page := range doc.Pages {
		// first is where this page starts. The rows before it are the pages
		// above, which hold the row of a detail that opens a page.
		first := len(rows)
		rows = append(rows, g.pageRows(i+1, page)...)
		g.attach(rows, first, page)
	}
	return rows
}

// pageRows pairs each date with the amount on its y. Blocks do not arrive in
// row order, so the pair is found by coordinate and never by sequence. A date
// with no amount on its y produces no row.
func (g Geometry) pageRows(number int, page Page) []Row {
	var dates, amounts []Block
	for _, b := range page.Blocks {
		switch {
		case g.IsDate(b):
			dates = append(dates, b)
		case g.IsAmount(b):
			amounts = append(amounts, b)
		}
	}
	sort.SliceStable(dates, func(i, j int) bool { return dates[i].YMin < dates[j].YMin })

	taken := make([]bool, len(amounts))
	rows := make([]Row, 0, len(dates))
	for _, d := range dates {
		for i, a := range amounts {
			if taken[i] || a.YMin != d.YMin || a.YMax != d.YMax {
				continue
			}
			taken[i] = true
			rows = append(rows, Row{
				Page:   number,
				Y:      d.YMin,
				Date:   d.Lines[0].Text(),
				Amount: a.Lines[0].Text(),
			})
			break
		}
	}
	return rows
}

// attach gives each description block to its row. The description of a row is
// about one point off the row's y, so the match is a tolerance and not an
// equality.
func (g Geometry) attach(rows []Row, first int, page Page) {
	blocks := append([]Block(nil), page.Blocks...)
	sortByPosition(blocks)

	for _, b := range blocks {
		if !g.IsDescription(b) {
			continue
		}
		if i, ok := g.rowAt(rows, first, b.YMin); ok {
			rows[i].Description = appendLines(rows[i].Description, b.Lines)
			continue
		}
		// A detail and an exchange sub-line start at the left edge of the
		// column. The page furniture between the columns does not.
		if math.Abs(b.XMin-g.DescriptionLeftX) > g.LeftEdgeTolerance {
			continue
		}
		i, ok := rowAbove(rows, first, b.YMin)
		if !ok {
			continue
		}
		if beside, found := g.amountBeside(page, b.YMin); found {
			rows[i].Exchange = appendLines(rows[i].Exchange, b.Lines)
			rows[i].Exchange = appendLines(rows[i].Exchange, beside.Lines)
			continue
		}
		rows[i].Detail = appendLines(rows[i].Detail, b.Lines)
	}
}

// rowAt finds the row at a y, inside the tolerance. It returns the nearest.
func (g Geometry) rowAt(rows []Row, first int, y float64) (int, bool) {
	best, distance := -1, g.YTolerance
	for i := first; i < len(rows); i++ {
		if d := math.Abs(rows[i].Y - y); d <= distance {
			best, distance = i, d
		}
	}
	return best, best >= 0
}

// rowAbove finds the row a detail belongs to: the last row above it on this
// page, or the last row of the pages before. A detail that opens the document
// has no row, and it is discarded.
func rowAbove(rows []Row, first int, y float64) (int, bool) {
	for i := len(rows) - 1; i >= first; i-- {
		if rows[i].Y < y {
			return i, true
		}
	}
	return first - 1, first > 0
}

// amountBeside finds an amount-column block on a y that no row holds. It is
// the origin amount of an exchange sub-line (D56).
func (g Geometry) amountBeside(page Page, y float64) (Block, bool) {
	for _, b := range page.Blocks {
		if b.XMin > g.AmountColumnMinX && math.Abs(b.YMin-y) <= g.YTolerance {
			return b, true
		}
	}
	return Block{}, false
}

// appendLines adds the text of each line in reading order. Two lines of one
// block can share a y, because poppler splits a line on a wide gap.
func appendLines(dst []string, lines []Line) []string {
	ordered := append([]Line(nil), lines...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].YMin != ordered[j].YMin {
			return ordered[i].YMin < ordered[j].YMin
		}
		return ordered[i].XMin < ordered[j].XMin
	})
	for _, l := range ordered {
		if text := l.Text(); text != "" {
			dst = append(dst, text)
		}
	}
	return dst
}

func sortByPosition(blocks []Block) {
	sort.SliceStable(blocks, func(i, j int) bool {
		if blocks[i].YMin != blocks[j].YMin {
			return blocks[i].YMin < blocks[j].YMin
		}
		return blocks[i].XMin < blocks[j].XMin
	})
}
