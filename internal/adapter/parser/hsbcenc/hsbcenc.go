// Package hsbcenc inverts the Type 3 encoding of an HSBC México statement.
//
// Xenos D2eVision writes custom fonts with no ToUnicode map. Glyph names are
// EXNNN000, and NNN is the Windows-1252 code of the character. pdftotext emits
// the encoding bytes; this package turns them back into text (D74).
package hsbcenc

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
)

// ErrNoTable reports a PDF that has no HSBC Differences array.
var ErrNoTable = errors.New("hsbcenc: the artifact has no HSBC encoding table")

// ErrChanged reports a Differences array that is not EXNNN000 names.
var ErrChanged = errors.New("hsbcenc: the encoding table is not the HSBC shape")

var (
	differences = regexp.MustCompile(`/Differences\s*\[(.*?)\]`)
	token       = regexp.MustCompile(`(\d+)|/([A-Za-z0-9]+)`)
	glyph       = regexp.MustCompile(`^EX(\d{3})000$`)
)

// Table maps one encoding byte to a character. It is a value, so a test can
// build one without a PDF.
type Table [256]rune

// FromPDF reads the first /Differences array and builds a Table. Every Type 3
// font on the June and July 2026 statements carried the same array.
func FromPDF(raw []byte) (Table, error) {
	var t Table
	match := differences.FindSubmatch(raw)
	if match == nil {
		return t, ErrNoTable
	}
	code := -1
	mapped := 0
	for _, tok := range token.FindAllSubmatch(match[1], -1) {
		if len(tok[1]) > 0 {
			n, err := strconv.Atoi(string(tok[1]))
			if err != nil || n < 0 || n > 255 {
				return Table{}, fmt.Errorf("%w: a code is out of range", ErrChanged)
			}
			code = n
			continue
		}
		if code < 0 {
			return Table{}, fmt.Errorf("%w: a glyph precedes its code", ErrChanged)
		}
		ch, err := runeOf(string(tok[2]))
		if err != nil {
			return Table{}, err
		}
		t[code] = ch
		mapped++
		code++
		if code > 255 {
			code = -1
		}
	}
	if mapped < 32 {
		return Table{}, fmt.Errorf("%w: the table is too small", ErrChanged)
	}
	return t, nil
}

func runeOf(name string) (rune, error) {
	m := glyph.FindStringSubmatch(name)
	if m == nil {
		return 0, fmt.Errorf("%w: glyph %s", ErrChanged, name)
	}
	n, _ := strconv.Atoi(m[1])
	if n == 0 {
		return 0, nil
	}
	if n > 255 {
		return 0, fmt.Errorf("%w: glyph %s", ErrChanged, name)
	}
	return windows1252[n], nil
}

// Decode maps each byte of s. Characters already outside the encoding stay.
//
// pdftotext writes some bytes as Latin-1 and some as the cp1252 character of
// that byte. Both forms must invert to the same letter.
func (t Table) Decode(s string) string {
	out := make([]rune, 0, len(s))
	for _, ch := range s {
		if ch == '\n' || ch == '\r' || ch == '\t' {
			out = append(out, ch)
			continue
		}
		if ch < 256 && t[ch] != 0 {
			out = append(out, t[ch])
			continue
		}
		if b, ok := windows1252Byte[ch]; ok && t[b] != 0 {
			out = append(out, t[b])
			continue
		}
		if ch < 256 && t[ch] == 0 {
			continue
		}
		out = append(out, ch)
	}
	return string(out)
}

// DecodeDocument replaces every word in place. Package bbox has already parsed
// the coordinates; only the text is encoded.
func (t Table) DecodeDocument(doc *bbox.Document) {
	if doc == nil {
		return
	}
	for i := range doc.Pages {
		for j := range doc.Pages[i].Blocks {
			for k := range doc.Pages[i].Blocks[j].Lines {
				for l := range doc.Pages[i].Blocks[j].Lines[k].Words {
					w := &doc.Pages[i].Blocks[j].Lines[k].Words[l]
					w.Text = t.Decode(w.Text)
				}
			}
		}
	}
}

// windows1252 is byte → Unicode for the codes EXNNN000 stores. 0x80–0x9F
// follow Windows-1252, not Latin-1.
var windows1252 = [256]rune{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1A, 0x1B, 0x1C, 0x1D, 0x1E, 0x1F,
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27,
	0x28, 0x29, 0x2A, 0x2B, 0x2C, 0x2D, 0x2E, 0x2F,
	0x30, 0x31, 0x32, 0x33, 0x34, 0x35, 0x36, 0x37,
	0x38, 0x39, 0x3A, 0x3B, 0x3C, 0x3D, 0x3E, 0x3F,
	0x40, 0x41, 0x42, 0x43, 0x44, 0x45, 0x46, 0x47,
	0x48, 0x49, 0x4A, 0x4B, 0x4C, 0x4D, 0x4E, 0x4F,
	0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57,
	0x58, 0x59, 0x5A, 0x5B, 0x5C, 0x5D, 0x5E, 0x5F,
	0x60, 0x61, 0x62, 0x63, 0x64, 0x65, 0x66, 0x67,
	0x68, 0x69, 0x6A, 0x6B, 0x6C, 0x6D, 0x6E, 0x6F,
	0x70, 0x71, 0x72, 0x73, 0x74, 0x75, 0x76, 0x77,
	0x78, 0x79, 0x7A, 0x7B, 0x7C, 0x7D, 0x7E, 0x7F,
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
	0xA0, 0xA1, 0xA2, 0xA3, 0xA4, 0xA5, 0xA6, 0xA7,
	0xA8, 0xA9, 0xAA, 0xAB, 0xAC, 0xAD, 0xAE, 0xAF,
	0xB0, 0xB1, 0xB2, 0xB3, 0xB4, 0xB5, 0xB6, 0xB7,
	0xB8, 0xB9, 0xBA, 0xBB, 0xBC, 0xBD, 0xBE, 0xBF,
	0xC0, 0xC1, 0xC2, 0xC3, 0xC4, 0xC5, 0xC6, 0xC7,
	0xC8, 0xC9, 0xCA, 0xCB, 0xCC, 0xCD, 0xCE, 0xCF,
	0xD0, 0xD1, 0xD2, 0xD3, 0xD4, 0xD5, 0xD6, 0xD7,
	0xD8, 0xD9, 0xDA, 0xDB, 0xDC, 0xDD, 0xDE, 0xDF,
	0xE0, 0xE1, 0xE2, 0xE3, 0xE4, 0xE5, 0xE6, 0xE7,
	0xE8, 0xE9, 0xEA, 0xEB, 0xEC, 0xED, 0xEE, 0xEF,
	0xF0, 0xF1, 0xF2, 0xF3, 0xF4, 0xF5, 0xF6, 0xF7,
	0xF8, 0xF9, 0xFA, 0xFB, 0xFC, 0xFD, 0xFE, 0xFF,
}

var windows1252Byte = func() map[rune]byte {
	m := make(map[rune]byte, 256)
	for i, r := range windows1252 {
		if r != 0 {
			m[r] = byte(i)
		}
	}
	return m
}()
