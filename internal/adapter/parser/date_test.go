package parser_test

import (
	"errors"
	"testing"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
)

func TestParseWallAccepts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want parser.Wall
	}{
		{
			name: "outflow, Spanish month abbreviation",
			in:   "16/AGO/2026",
			want: parser.Wall{Year: 2026, Month: time.August, Day: 16},
		},
		{
			name: "inflow, spaced Spanish month",
			in:   "18 AGO 2026",
			want: parser.Wall{Year: 2026, Month: time.August, Day: 18},
		},
		{
			name: "legacy outflow, all numeric and day first",
			in:   "20/07/2026",
			want: parser.Wall{Year: 2026, Month: time.July, Day: 20},
		},
		{
			name: "service receipt, lowercase month and a clock",
			in:   "18 jul 2026 - 10:03:51",
			want: parser.Wall{Year: 2026, Month: time.July, Day: 18, Hour: 10, Minute: 3, Second: 51, HasTime: true},
		},
		{
			name: "mixed case month",
			in:   "01/Ene/2024",
			want: parser.Wall{Year: 2024, Month: time.January, Day: 1},
		},
		{
			name: "single-digit day",
			in:   "9/8/2026",
			want: parser.Wall{Year: 2026, Month: time.August, Day: 9},
		},
		{
			name: "surrounding whitespace is not part of the value",
			in:   "  16/AGO/2026\t",
			want: parser.Wall{Year: 2026, Month: time.August, Day: 16},
		},
		{
			name: "leap day exists in a leap year",
			in:   "29/FEB/2024",
			want: parser.Wall{Year: 2024, Month: time.February, Day: 29},
		},
		{
			name: "every Spanish abbreviation resolves",
			in:   "15/DIC/2025",
			want: parser.Wall{Year: 2025, Month: time.December, Day: 15},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.ParseWall(tt.in)
			if err != nil {
				t.Fatalf("ParseWall(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("ParseWall(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

// All twelve abbreviations, because a month table with a typo in it fails on
// one twelfth of the corpus and looks like an answer on the rest.
func TestParseWallEveryMonth(t *testing.T) {
	abbrevs := []string{"ENE", "FEB", "MAR", "ABR", "MAY", "JUN", "JUL", "AGO", "SEP", "OCT", "NOV", "DIC"}
	for i, a := range abbrevs {
		got, err := parser.ParseWall("15/" + a + "/2026")
		if err != nil {
			t.Errorf("ParseWall(%q): %v", a, err)
			continue
		}
		if want := time.Month(i + 1); got.Month != want {
			t.Errorf("%s parsed as %v, want %v", a, got.Month, want)
		}
	}
}

func TestParseWallRefuses(t *testing.T) {
	for _, in := range []string{
		"",
		"Completada",
		"AGO/2026",
		"16/AGO",
		"16/AGO/26",     // two-digit years are not in the corpus and would be a guess
		"2026-08-16",    // ISO is not a format any template uses
		"16/AUG/2026",   // English month names are not Spanish ones
		"16/SEPT/2026",  // four letters is not the abbreviation Nu writes
		"32/AGO/2026",   // no such day
		"30/FEB/2026",   // time.Date would normalise this to 2 March
		"29/FEB/2026",   // 2026 is not a leap year
		"00/AGO/2026",   // no zeroth day
		"16/13/2026",    // no thirteenth month
		"16/00/2026",    // no zeroth month
		"16/AGO/2026 x", // trailing junk
	} {
		t.Run(in, func(t *testing.T) {
			if got, err := parser.ParseWall(in); err == nil {
				t.Fatalf("ParseWall(%q) = %v, want an error", in, got)
			} else if !errors.Is(err, parser.ErrNotDate) {
				t.Errorf("ParseWall(%q) error = %v, want ErrNotDate", in, err)
			}
		})
	}
}

func TestParseClock(t *testing.T) {
	ok := []struct {
		in      string
		h, m, s int
	}{
		{"17:44", 17, 44, 0},
		{"00:00", 0, 0, 0},
		{"23:59", 23, 59, 0},
		{"10:03:51", 10, 3, 51},
		{"9:05", 9, 5, 0},
	}
	for _, tt := range ok {
		h, m, s, err := parser.ParseClock(tt.in)
		if err != nil {
			t.Errorf("ParseClock(%q): %v", tt.in, err)
			continue
		}
		if h != tt.h || m != tt.m || s != tt.s {
			t.Errorf("ParseClock(%q) = %d:%d:%d, want %d:%d:%d", tt.in, h, m, s, tt.h, tt.m, tt.s)
		}
	}

	// "07:44 pm" would be a silent twelve-hour error, so it is not accepted.
	for _, in := range []string{"", "24:00", "17:60", "10:03:60", "5", "17.44", "07:44 pm", "1744"} {
		if _, _, _, err := parser.ParseClock(in); err == nil {
			t.Errorf("ParseClock(%q) accepted", in)
		}
	}
}

// The zone is the caller's, and this is the whole reason Wall is not a
// time.Time: read as UTC, a 21:16 Mexico City transfer on 20 July lands on the
// 21st, and a month-end one lands in the next month.
func TestWallCarriesNoZoneOfItsOwn(t *testing.T) {
	w, err := parser.ParseWall("20/07/2026")
	if err != nil {
		t.Fatal(err)
	}
	w = w.WithClock(21, 16, 0)

	mexico := time.FixedZone("CST", -6*60*60)
	local := w.In(mexico)
	if got := local.UTC(); got.Day() != 21 || got.Hour() != 3 {
		t.Errorf("21:16 CST on the 20th is %v UTC, want the 21st at 03:16", got)
	}
	if got := w.In(time.UTC); got.Day() != 20 || got.Hour() != 21 {
		t.Errorf("In(UTC) = %v, want the wall value unchanged", got)
	}
}

func TestWallString(t *testing.T) {
	w, err := parser.ParseWall("16/AGO/2026")
	if err != nil {
		t.Fatal(err)
	}
	if got := w.String(); got != "2026-08-16" {
		t.Errorf("String() = %q, want %q", got, "2026-08-16")
	}
	if got := w.WithClock(17, 44, 0).String(); got != "2026-08-16 17:44:00" {
		t.Errorf("String() = %q, want %q", got, "2026-08-16 17:44:00")
	}
	if w.HasTime {
		t.Error("WithClock mutated the receiver; Wall is a value")
	}
}
