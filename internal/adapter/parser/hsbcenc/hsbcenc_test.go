package hsbcenc_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/bbox"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/hsbcenc"
)

func TestFromPDFRejectsADocumentWithNoTable(t *testing.T) {
	_, err := hsbcenc.FromPDF([]byte("%PDF-1.7\n1 0 obj"))
	if !errors.Is(err, hsbcenc.ErrNoTable) {
		t.Fatalf("err = %v, want ErrNoTable", err)
	}
}

func TestFromPDFRejectsAChangedTable(t *testing.T) {
	raw := []byte("%PDF-1.7 /Differences [32 /space /A /B /C /D /E /F /G /H /I /J /K /L /M /N /O /P /Q /R /S /T /U /V /W /X /Y /Z /a /b /c /d]")
	_, err := hsbcenc.FromPDF(raw)
	if !errors.Is(err, hsbcenc.ErrChanged) {
		t.Fatalf("err = %v, want ErrChanged", err)
	}
}

func TestFromPDFReadsTheHSBCGlyphNames(t *testing.T) {
	table, err := hsbcenc.FromPDF(identityDifferences())
	if err != nil {
		t.Fatalf("FromPDF: %v", err)
	}
	if table[67] != 'C' || table[101] != 'e' {
		t.Fatalf("C=%q e=%q", table[67], table[101])
	}
	if got := table.Decode(string([]byte{67, 85, 69, 78, 84, 65})); got != "CUENTA" {
		t.Errorf("Decode = %q", got)
	}
}

func TestDecodeLeavesUnknownCharacters(t *testing.T) {
	var table hsbcenc.Table
	table[65] = 'A'
	if got := table.Decode("Aβ"); got != "Aβ" {
		t.Errorf("Decode = %q", got)
	}
}

func TestDecodeDocumentReplacesEveryWord(t *testing.T) {
	table, err := hsbcenc.FromPDF(identityDifferences())
	if err != nil {
		t.Fatal(err)
	}
	encoded := string([]byte{72, 83, 66, 67})
	doc, err := bbox.Parse([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body><doc>
<page width="612" height="792"><flow>
<block xMin="1" yMin="1" xMax="2" yMax="2">
<line xMin="1" yMin="1" xMax="2" yMax="2">
<word xMin="1" yMin="1" xMax="2" yMax="2">` + encoded + `</word>
</line></block>
</flow></page></doc></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	table.DecodeDocument(doc)
	if got := doc.Pages[0].Blocks[0].Lines[0].Words[0].Text; got != "HSBC" {
		t.Errorf("word = %q", got)
	}
}

func TestJuneAndJulyShareOneTable(t *testing.T) {
	june, july, ok := statementPDFs(t)
	if !ok {
		t.Skip("HSBC statement PDFs are not in this checkout")
	}
	a, err := hsbcenc.FromPDF(june)
	if err != nil {
		t.Fatalf("june: %v", err)
	}
	b, err := hsbcenc.FromPDF(july)
	if err != nil {
		t.Fatalf("july: %v", err)
	}
	if a != b {
		t.Fatal("June and July encoding tables differ")
	}
	if a[0x80] == 0 && a[67] == 0 {
		t.Fatal("the shared table maps nothing")
	}
}

func identityDifferences() []byte {
	var b strings.Builder
	b.WriteString("%PDF-1.7 /Differences [32")
	for i := 32; i <= 126; i++ {
		b.WriteString(" /EX")
		if i < 100 {
			b.WriteByte('0')
		}
		if i < 10 {
			b.WriteByte('0')
		}
		b.WriteString(itoa(i))
		b.WriteString("000")
	}
	b.WriteString("]")
	return []byte(b.String())
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [4]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func statementPDFs(t *testing.T) (june, july []byte, ok bool) {
	t.Helper()
	dirs := []string{
		os.Getenv("HSBC_STATEMENT_DIR"),
		"/home/ubuntu/.cursor/projects/workspace/uploads",
		filepath.Join("testdata", "statements"),
		"/workspace/testdata/statements",
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		j6, err6 := findPDF(dir, "2026-06-30")
		j7, err7 := findPDF(dir, "2026-07-31")
		if err6 == nil && err7 == nil {
			return j6, j7, true
		}
	}
	return nil, nil, false
}

func findPDF(dir, prefix string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(strings.ToLower(name), ".pdf") {
			return os.ReadFile(filepath.Join(dir, name))
		}
	}
	return nil, os.ErrNotExist
}
