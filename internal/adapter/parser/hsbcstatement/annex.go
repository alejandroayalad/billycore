package hsbcstatement

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/domain"
)

var (
	annexDate  = regexp.MustCompile(`^\d{2}/\d{2}/\d{4}$`)
	annexClock = regexp.MustCompile(`^\d{2}:\d{2}:\d{2}$`)
	annexZero  = regexp.MustCompile(`^0+$`)
)

const (
	keyMinX = 460
	keyMaxX = 515
)

type annexRow struct {
	day       time.Time
	clock     string
	amount    string
	direction domain.TransactionDirection
	key       string
}

type joinKey struct {
	day   string
	minor int64
	dir   domain.TransactionDirection
}

func enrich(claims []map[domain.FieldName]domain.ClaimField, doc *bbox.Document) error {
	rows := annexRows(doc)
	if len(rows) == 0 {
		return nil
	}
	index := make(map[joinKey][]int)
	for i, fields := range claims {
		k, ok := claimJoin(fields)
		if !ok {
			continue
		}
		index[k] = append(index[k], i)
	}
	for _, row := range rows {
		money, err := parser.ParseMoney(row.amount, Currency)
		if err != nil {
			return err
		}
		k := joinKey{day: row.day.Format("2006-01-02"), minor: money.Minor(), dir: row.direction}
		hits := index[k]
		if len(hits) != 1 {
			continue
		}
		fields := claims[hits[0]]
		if row.key != "" {
			key, err := domain.NewTextField(row.key, domain.High)
			if err != nil {
				return fmt.Errorf("hsbcstatement: %s: %w", templateChanged, err)
			}
			fields[domain.FieldTrackingKey] = key
		}
		if row.clock != "" {
			when, err := occurredAt(row.day, row.clock)
			if err != nil {
				return err
			}
			at, err := domain.NewTimeField(when, domain.High)
			if err != nil {
				return fmt.Errorf("hsbcstatement: %s: %w", templateChanged, err)
			}
			fields[domain.FieldOccurredAt] = at
		}
	}
	return nil
}

func claimJoin(fields map[domain.FieldName]domain.ClaimField) (joinKey, bool) {
	amount, ok := fields[domain.FieldAmountMinor]
	if !ok || !amount.IsInt() {
		return joinKey{}, false
	}
	dir, ok := fields[domain.FieldDirection]
	if !ok {
		return joinKey{}, false
	}
	at, ok := fields[domain.FieldOccurredAt]
	if !ok {
		return joinKey{}, false
	}
	when, err := time.Parse(domain.TimeLayout, at.Text())
	if err != nil {
		return joinKey{}, false
	}
	day := when.In(zone).Format("2006-01-02")
	return joinKey{day: day, minor: amount.Int(), dir: domain.TransactionDirection(dir.Text())}, true
}

func annexRows(doc *bbox.Document) []annexRow {
	var words []placed
	for i, page := range doc.Pages {
		for _, b := range page.Blocks {
			for _, line := range b.Lines {
				for _, w := range line.Words {
					text := strings.TrimSpace(w.Text)
					if text == "" {
						continue
					}
					words = append(words, placed{page: i + 1, x: w.XMin, y: w.YMin, text: text})
				}
			}
		}
	}
	sort.SliceStable(words, func(i, j int) bool {
		return placedLess(words[i], words[j])
	})

	var (
		section domain.TransactionDirection
		out     []annexRow
	)
	for i, w := range words {
		if next, ok := sectionOf(w.text); ok {
			section = next
			continue
		}
		if section == "" || !annexDate.MatchString(w.text) {
			continue
		}
		day, err := parser.ParseWall(w.text)
		if err != nil {
			continue
		}
		row := annexRow{day: day.In(zone), direction: section}
		for _, other := range words[i+1:] {
			if other.page != w.page {
				break
			}
			if other.y-w.y > 22 {
				break
			}
			if annexDate.MatchString(other.text) {
				break
			}
			if annexClock.MatchString(other.text) && row.clock == "" {
				row.clock = other.text
				continue
			}
			if strings.HasPrefix(other.text, "$") && row.amount == "" {
				row.amount = other.text
				continue
			}
			if other.x >= keyMinX && other.x < keyMaxX && !annexZero.MatchString(other.text) {
				row.key += other.text
			}
		}
		if row.amount == "" {
			continue
		}
		row.key = normaliseKey(row.key)
		out = append(out, row)
	}
	return out
}

func sectionOf(text string) (domain.TransactionDirection, bool) {
	if strings.Contains(text, "CoDi") {
		return "", true
	}
	if !strings.Contains(text, "SPEI") {
		return "", false
	}
	if strings.Contains(text, "Enviados") {
		return domain.Outflow, true
	}
	if strings.Contains(text, "Recib") || strings.Contains(text, "SPEI´s R") || strings.Contains(text, "SPEI's R") {
		return domain.Inflow, true
	}
	return "", false
}

func normaliseKey(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "HSBC") {
		return s
	}
	if strings.HasPrefix(s, "HSB") && len(s) > 3 && s[3] >= '0' && s[3] <= '9' {
		return "HSBC" + s[3:]
	}
	return s
}

type placed struct {
	page int
	x, y float64
	text string
}

func placedLess(a, b placed) bool {
	if a.page != b.page {
		return a.page < b.page
	}
	if a.y != b.y {
		return a.y < b.y
	}
	return a.x < b.x
}
