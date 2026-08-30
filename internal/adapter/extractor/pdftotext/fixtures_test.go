package pdftotext

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
)

// The fixtures are redacted windows of `pdftotext -bbox-layout` output from
// the May, June and July 2026 statements. Coordinates and structure are the
// real ones; every value is synthetic (D54). See testdata/README.md.
type (
	coordinateDocument = bbox.Document
	coordinateBlock    = bbox.Block
)

// nu is the geometry of the statement: where the three columns are, and what
// a date and an amount look like inside them.
var nu = bbox.NuStatementV1()

var digitRun = regexp.MustCompile(`\d+`)

func readFixture(t *testing.T, name string) *coordinateDocument {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// The standard library is the whole parser. D54 adds no dependency.
	doc, err := bbox.Parse(data)
	if err != nil {
		t.Fatalf("parse coordinate XML: %v", err)
	}
	return doc
}

func fixtureNames(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", "*.xhtml"))
	if err != nil {
		t.Fatalf("fixture glob: %v", err)
	}
	if len(paths) != 4 {
		t.Fatalf("fixture count = %d, want 4", len(paths))
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	return names
}

// The redaction builds every long number from a repeated two-digit motif. A
// real account, CLABE, card or tracking key never looks like that, so this
// check proves mechanically that none of them survived.
func TestCoordinateFixturesAreSmallAndRedacted(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("read fixture: %v", err)
			}
			if len(data) > 32<<10 {
				t.Fatalf("fixture is %d bytes; a fixture is a window, not a statement", len(data))
			}
			if !strings.Contains(string(data), "SYNTHETIC") {
				t.Fatal("fixture carries no synthetic marker")
			}
			doc := readFixture(t, name)
			for _, page := range doc.Pages {
				for _, block := range page.Blocks {
					for _, line := range block.Lines {
						for _, word := range line.Words {
							for _, run := range digitRun.FindAllString(word.Text, -1) {
								if len(run) >= 7 && distinctDigits(run) > 2 {
									t.Fatalf("word %q holds a real identifier", word.Text)
								}
							}
						}
					}
				}
			}
		})
	}
}

func distinctDigits(run string) int {
	seen := map[rune]bool{}
	for _, r := range run {
		seen[r] = true
	}
	return len(seen)
}

func TestCoordinateFixturesKeepPageAndWordCoordinates(t *testing.T) {
	for _, name := range fixtureNames(t) {
		t.Run(name, func(t *testing.T) {
			doc := readFixture(t, name)
			if len(doc.Pages) == 0 {
				t.Fatal("fixture holds no page")
			}
			for _, page := range doc.Pages {
				if page.Width <= 0 || page.Height <= 0 {
					t.Fatalf("page size = %vx%v, want positive", page.Width, page.Height)
				}
				if len(page.Blocks) == 0 {
					t.Fatal("page holds no block")
				}
				for _, block := range page.Blocks {
					if len(block.Lines) == 0 {
						t.Fatal("block holds no line")
					}
					for _, line := range block.Lines {
						if len(line.Words) == 0 {
							t.Fatal("line holds no word")
						}
						for _, word := range line.Words {
							if word.XMax <= word.XMin || word.YMax <= word.YMin {
								t.Fatalf("word %q has no bounding box", word.Text)
							}
						}
					}
				}
			}
		})
	}
}

// This is the measurement D54 rests on. The date and the amount are separate
// blocks, and they carry the same y. That pair is what identifies a row.
func TestMayFixturePairsEveryDateWithAnAmountOnTheSameY(t *testing.T) {
	blocks := readFixture(t, "nu_2026_05_bbox.xhtml").Pages[0].Blocks

	var dates, amounts []coordinateBlock
	for _, b := range blocks {
		switch {
		case nu.IsDate(b):
			dates = append(dates, b)
		case nu.IsAmount(b):
			amounts = append(amounts, b)
		}
	}
	if len(dates) == 0 {
		t.Fatal("fixture holds no date block")
	}
	if len(dates) != len(amounts) {
		t.Fatalf("dates = %d, amounts = %d, want one amount for each date", len(dates), len(amounts))
	}

	for _, d := range dates {
		matched := false
		for _, a := range amounts {
			if d.YMin == a.YMin && d.YMax == a.YMax {
				matched = true
				break
			}
		}
		if !matched {
			t.Fatalf("date block at y=%v has no amount on the same y", d.YMin)
		}
	}
}

// pdftotext does not emit blocks in row order. A parser that reads the blocks
// in sequence pairs the wrong date with the wrong amount.
func TestMayFixtureBlocksAreNotInRowOrder(t *testing.T) {
	blocks := readFixture(t, "nu_2026_05_bbox.xhtml").Pages[0].Blocks

	descending := false
	for i := 1; i < len(blocks); i++ {
		if blocks[i].YMin < blocks[i-1].YMin {
			descending = true
			break
		}
	}
	if !descending {
		t.Fatal("blocks arrive sorted by y; the fixture no longer shows that order is not row order")
	}
}

// The SPEI detail wraps over several lines, and two of them can share a y
// because poppler splits a line where the horizontal gap is wide.
func TestMayFixtureHoldsAWrappedDetailWithTwoLinesOnOneY(t *testing.T) {
	blocks := readFixture(t, "nu_2026_05_bbox.xhtml").Pages[0].Blocks

	for _, b := range blocks {
		if !nu.IsDescription(b) || len(b.Lines) < 3 {
			continue
		}
		seen := map[float64]int{}
		for _, l := range b.Lines {
			if strings.TrimSpace(l.Text()) == "" {
				t.Fatal("a detail line holds no description candidate")
			}
			seen[l.YMin]++
		}
		for y, n := range seen {
			if n > 1 {
				t.Logf("block at y=%v has %d lines sharing y=%v", b.YMin, n, y)
				return
			}
		}
	}
	t.Fatal("fixture holds no wrapped detail with two lines on one y")
}

// Plain `-layout` misplaced five of these. Every line of the detail must stay
// inside one block, below the dated row it belongs to (D54).
func TestJuneFixtureHoldsAWrappedDetailBlock(t *testing.T) {
	blocks := readFixture(t, "nu_2026_06_bbox.xhtml").Pages[0].Blocks

	for _, b := range blocks {
		if !nu.IsDescription(b) || len(b.Lines) < 3 {
			continue
		}
		for i := 1; i < len(b.Lines); i++ {
			if b.Lines[i].YMin < b.Lines[i-1].YMin {
				t.Fatalf("detail line %d is above the line before it", i)
			}
		}
		if strings.TrimSpace(b.Lines[len(b.Lines)-1].Text()) == "" {
			t.Fatal("the last detail line is empty")
		}
		return
	}
	t.Fatal("fixture holds no wrapped detail block")
}

// A row's detail can continue on the next page. The detail block arrives
// before any date on that page, so the parser must carry the open row over.
func TestJulyFixtureContinuesADetailOnTheNextPage(t *testing.T) {
	doc := readFixture(t, "nu_2026_07_bbox.xhtml")
	if len(doc.Pages) != 2 {
		t.Fatalf("page count = %d, want 2", len(doc.Pages))
	}

	first := doc.Pages[0].Blocks
	if !nu.IsDescription(first[len(first)-1]) && len(first[len(first)-1].Lines) == 0 {
		t.Fatal("the first page does not end on page furniture")
	}

	var detailIndex, dateIndex = -1, -1
	for i, b := range doc.Pages[1].Blocks {
		if detailIndex < 0 && nu.IsDescription(b) && len(b.Lines) >= 3 {
			detailIndex = i
		}
		if dateIndex < 0 && nu.IsDate(b) {
			dateIndex = i
		}
	}
	if detailIndex < 0 {
		t.Fatal("the second page holds no detail block")
	}
	if dateIndex < 0 {
		t.Fatal("the second page holds no dated row")
	}
	if detailIndex > dateIndex {
		t.Fatal("the detail block follows the first date, so no detail crosses the page")
	}
}
