package parser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
)

// The fixture is synthetic, and deliberately combines the three things the
// corpus makes real: multipart/mixed around the body, an iso-8859-1 charset on
// the legacy outflow template, and an attachment whose declared filename is a
// path traversal.
func TestHTMLBodyFromMultipartLatin1(t *testing.T) {
	raw := readFixture(t, "mime_latin1_multipart.eml")

	body, err := parser.HTMLBody(raw)
	if err != nil {
		t.Fatalf("HTMLBody: %v", err)
	}
	text, err := parser.Text(body)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}

	// 163 transaction-bearing artifacts are latin-1. Read as UTF-8, every
	// accented label in them arrives corrupted and anchors on nothing.
	for _, want := range []string{"Monto: $498.00", "Fecha: 20/07/2026", "Hora: 21:16", "Número de referencia"} {
		if !strings.Contains(text, want) {
			t.Errorf("extracted text is missing %q\ngot:\n%s", want, text)
		}
	}
	if strings.Contains(text, "�") {
		t.Errorf("latin-1 body was decoded as UTF-8: %q", text)
	}
	if strings.Contains(text, "var x") || strings.Contains(text, "ignorado") {
		t.Errorf("script or title leaked into the text: %q", text)
	}
	if strings.Contains(string(body), "JVBERi0") {
		t.Error("the PDF attachment was returned as part of the body")
	}
}

func TestHeaderDecodesEncodedWords(t *testing.T) {
	raw := readFixture(t, "mime_latin1_multipart.eml")

	got, err := parser.Header(raw, "Subject")
	if err != nil {
		t.Fatalf("Header: %v", err)
	}
	if want := "Tu transferencia fue exitosa"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if got, err := parser.Header(raw, "X-Absent"); err != nil || got != "" {
		t.Errorf("absent header = %q, %v; want empty and no error", got, err)
	}
}

func TestHTMLBodyRefuses(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"no html part", "Content-Type: text/plain\r\n\r\nhola"},
		{"no content type", "From: a@example.test\r\n\r\nhola"},
		{"multipart with no boundary", "Content-Type: multipart/mixed\r\n\r\nhola"},
		{"not a message", "\x00\x01\x02"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := parser.HTMLBody([]byte(tt.raw)); err == nil {
				t.Fatalf("HTMLBody accepted %q, returning %q", tt.raw, got)
			}
		})
	}

	if _, err := parser.HTMLBody(make([]byte, parser.MaxInputBytes+1)); err != parser.ErrTooLarge {
		t.Errorf("oversized artifact: got %v, want ErrTooLarge", err)
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}
