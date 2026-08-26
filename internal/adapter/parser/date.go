package parser

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNotDate reports text that matches none of the known formats.
var ErrNotDate = errors.New("parser: not a recognised date")

// Wall is a date, and optionally a time of day, exactly as the email wrote it
// — with no time zone attached, because none was stated.
//
// # Why this is not a time.Time
//
// Nu writes "16/AGO/2026" and "Hora: 17:44" and names no zone anywhere.
// DATA_MODEL.md §2 stores occurred_at as UTC RFC 3339, so somewhere a zone has
// to be chosen; choosing it here, silently, would be the quietest possible
// error. Mexico City is UTC-6, so stamping these fields as UTC shifts every
// transaction six hours later — enough to move a late-evening transfer into
// the next day, and a month-end one into the next month, which is precisely
// the boundary the MVP's "last month of transactions" is drawn on.
//
// So Wall carries what was written and nothing more. The caller converts with
// [Wall.In] once the zone is a decision rather than an assumption (D21).
type Wall struct {
	Year   int
	Month  time.Month
	Day    int
	Hour   int
	Minute int
	Second int

	// HasTime distinguishes a date that carried a time of day from one that
	// did not. Midnight is a real time; absence is not midnight.
	HasTime bool
}

// In interprets the wall value in loc. The zone is the caller's decision and
// is stated at the call site, which is the entire point of the type.
func (w Wall) In(loc *time.Location) time.Time {
	return time.Date(w.Year, w.Month, w.Day, w.Hour, w.Minute, w.Second, 0, loc)
}

// WithClock returns w carrying the given time of day. Templates that split the
// timestamp across `Fecha:` and `Hora:` labels are joined this way.
func (w Wall) WithClock(hour, minute, second int) Wall {
	w.Hour, w.Minute, w.Second, w.HasTime = hour, minute, second, true
	return w
}

func (w Wall) String() string {
	if w.HasTime {
		return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", w.Year, int(w.Month), w.Day, w.Hour, w.Minute, w.Second)
	}
	return fmt.Sprintf("%04d-%02d-%02d", w.Year, int(w.Month), w.Day)
}

// months are the Spanish three-letter abbreviations Nu uses. Case varies by
// template — "AGO" on transfers, "jul" on service receipts — so lookups are
// folded. There is no full-name form in the corpus, and none is invented here.
var months = map[string]time.Month{
	"ene": time.January, "feb": time.February, "mar": time.March,
	"abr": time.April, "may": time.May, "jun": time.June,
	"jul": time.July, "ago": time.August, "sep": time.September,
	"oct": time.October, "nov": time.November, "dic": time.December,
}

// The four formats present in the corpus, measured 2026-08-25 over all 800
// transaction-bearing artifacts:
//
//	16/AGO/2026              282  outflow, current and legacy templates
//	18 AGO 2026              345  inflow
//	09/08/2026                72  outflow, legacy template only
//	18 jul 2026 - 10:03:51    11  service payment receipt
//
// The numeric form is the one the design did not know about. It is
// day-first: of the 72, 51 have a day above 12 and so are unambiguous, and
// all 72 agree with their artifact's own delivery timestamp to within a day.
// Read month-first they would not.
//
// An optional trailing clock is accepted on any of them because the service
// receipt carries one; the others simply never do.
var (
	dateAbbrevSlash = regexp.MustCompile(`^(\d{1,2})/([A-Za-z]{3})/(\d{4})$`)
	dateAbbrevSpace = regexp.MustCompile(`^(\d{1,2}) ([A-Za-z]{3}) (\d{4})$`)
	dateNumeric     = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
	clockPattern    = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::(\d{2}))?$`)
)

// ParseWall reads one of the four known date forms, with an optional
// " - HH:MM:SS" suffix.
func ParseWall(s string) (Wall, error) {
	text := strings.Join(strings.Fields(s), " ")

	clock := ""
	if i := strings.LastIndex(text, " - "); i >= 0 {
		clock = strings.TrimSpace(text[i+3:])
		text = strings.TrimSpace(text[:i])
	}

	var day, month, year int
	switch {
	case dateAbbrevSlash.MatchString(text):
		m := dateAbbrevSlash.FindStringSubmatch(text)
		day, year = atoi(m[1]), atoi(m[3])
		mon, ok := months[strings.ToLower(m[2])]
		if !ok {
			return Wall{}, fmt.Errorf("%w: %q: unknown month %q", ErrNotDate, s, m[2])
		}
		month = int(mon)

	case dateAbbrevSpace.MatchString(text):
		m := dateAbbrevSpace.FindStringSubmatch(text)
		day, year = atoi(m[1]), atoi(m[3])
		mon, ok := months[strings.ToLower(m[2])]
		if !ok {
			return Wall{}, fmt.Errorf("%w: %q: unknown month %q", ErrNotDate, s, m[2])
		}
		month = int(mon)

	case dateNumeric.MatchString(text):
		m := dateNumeric.FindStringSubmatch(text)
		day, month, year = atoi(m[1]), atoi(m[2]), atoi(m[3])

	default:
		return Wall{}, fmt.Errorf("%w: %q", ErrNotDate, s)
	}

	w, err := newWall(year, month, day)
	if err != nil {
		return Wall{}, fmt.Errorf("%w: %q: %v", ErrNotDate, s, err)
	}
	if clock == "" {
		return w, nil
	}
	h, mnt, sec, err := ParseClock(clock)
	if err != nil {
		return Wall{}, fmt.Errorf("%w: %q: %v", ErrNotDate, s, err)
	}
	return w.WithClock(h, mnt, sec), nil
}

// ParseClock reads a 24-hour "HH:MM" or "HH:MM:SS" time of day, the form every
// `Hora:` value in the corpus takes. There is no 12-hour or am/pm variant, and
// none is accepted: "07:44" meaning the evening would be a silent six-hour
// error of a different kind.
func ParseClock(s string) (hour, minute, second int, err error) {
	m := clockPattern.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, 0, 0, fmt.Errorf("%w: %q is not HH:MM", ErrNotDate, s)
	}
	hour, minute = atoi(m[1]), atoi(m[2])
	if m[3] != "" {
		second = atoi(m[3])
	}
	if hour > 23 || minute > 59 || second > 59 {
		return 0, 0, 0, fmt.Errorf("%w: %q is out of range", ErrNotDate, s)
	}
	return hour, minute, second, nil
}

// newWall rejects a date that does not exist. time.Date normalises 31 February
// into 3 March rather than failing, so the round-trip is the check: a value
// that comes back changed was never a real date.
func newWall(year, month, day int) (Wall, error) {
	if month < 1 || month > 12 {
		return Wall{}, fmt.Errorf("month %d out of range", month)
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day {
		return Wall{}, fmt.Errorf("%04d-%02d-%02d is not a calendar date", year, month, day)
	}
	return Wall{Year: year, Month: time.Month(month), Day: day}, nil
}

// atoi is safe here only because every caller passes a regexp-matched digit
// run of bounded length.
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
