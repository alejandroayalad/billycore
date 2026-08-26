package parser_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
)

func TestTextStructure(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "inline elements keep a label with its value",
			in:   `<li><strong>Monto:</strong> $299.00</li>`,
			want: "Monto: $299.00",
		},
		{
			name: "block elements separate unrelated cells",
			in:   `<td>Monto: $1,000.00</td><td>Folio: TESTFOLIO1</td>`,
			want: "Monto: $1,000.00\nFolio: TESTFOLIO1",
		},
		{
			name: "script content never reaches the text",
			in:   `<div>antes</div><script>var amount = "$9,999.99";</script><div>despues</div>`,
			want: "antes\ndespues",
		},
		{
			name: "style and head are discarded whole",
			in:   `<head><style>.x{content:"$1.00"}</style><title>Asunto</title></head><body>Monto: $2.00</body>`,
			want: "Monto: $2.00",
		},
		{
			name: "comments, including Outlook conditionals, are discarded",
			in:   `a<!--[if mso | IE]><td>$0.01</td><![endif]-->b<!-- ONLY CHANGE THE TEXT HERE -->c`,
			want: "abc",
		},
		{
			name: "a greater-than inside an attribute does not end the tag",
			in:   `<div style="font:13px; content:'a>b'" data-x="c>d">Monto: $5.00</div>`,
			want: "Monto: $5.00",
		},
		{
			name: "doctype is not text",
			in:   `<!doctype html><html><body>hola</body></html>`,
			want: "hola",
		},
		{
			name: "known entities are expanded and zwnj vanishes",
			in:   `<p>Nu&nbsp;&amp;&nbsp;Co&zwnj;&zwnj;</p><p>&lt;script&gt;</p>`,
			want: "Nu & Co\n<script>",
		},
		{
			name: "an unknown entity is left verbatim rather than guessed at",
			in:   `<p>10&euro; &mdash; y</p>`,
			want: "10&euro; &mdash; y",
		},
		{
			name: "literal nbsp and zwnj bytes are normalised too",
			in:   "<p>Monto: $7.00‌</p>",
			want: "Monto: $7.00",
		},
		{
			name: "whitespace collapses and empty lines are dropped",
			in:   "<div>  a\t\t b \r\n c  </div><div></div><div>d</div>",
			want: "a b c\nd",
		},
		{
			name: "a bare less-than is ordinary text",
			in:   `<p>1 < 2 y 3 > 2</p>`,
			want: "1 < 2 y 3 > 2",
		},
		{
			name: "an unterminated style element swallows the rest, never leaking CSS",
			in:   `<div>a</div><style>.x{color:red}`,
			want: "a",
		},
		{
			name: "an unterminated comment discards the rest",
			in:   `<div>a</div><!-- b`,
			want: "a",
		},
		{
			name: "empty input yields empty text",
			in:   ``,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.Text([]byte(tt.in))
			if err != nil {
				t.Fatalf("Text: %v", err)
			}
			if got != tt.want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tt.in, got, tt.want)
			}
		})
	}
}

// A tracking pixel must survive as nothing at all. SECURITY.md §7: a parser
// that resolved this would report every re-parse to Nu's ESP.
func TestTextDoesNotSurfaceRemoteReferences(t *testing.T) {
	const in = `<p>Monto: $1.00</p><img src="https://track.example.test/open?id=abc" width="1" height="1">` +
		`<a href="https://click.example.test/x">ver</a>`

	got, err := parser.Text([]byte(in))
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	if strings.Contains(got, "example.test") || strings.Contains(got, "http") {
		t.Errorf("Text leaked a remote reference into the text: %q", got)
	}
	if !strings.Contains(got, "ver") {
		t.Errorf("Text dropped the link's own text: %q", got)
	}
}

func TestTextRefusesOversizedInput(t *testing.T) {
	_, err := parser.Text(make([]byte, parser.MaxInputBytes+1))
	if !errors.Is(err, parser.ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

// The fixtures are real Nu bodies with names, folios, tracking keys and URLs
// replaced. Their value is that the markup is untouched.
func TestTextOnRealTemplates(t *testing.T) {
	tests := []struct {
		file  string
		lines []string
	}{
		{"outflow_rich.html", []string{
			"Monto: $1,000.00", "Fecha: 16/AGO/2026", "Hora: 17:44",
			"Folio: TESTFOLIO1", "Entidad: HSBC", "Estatus: Completada",
		}},
		{"outflow_legacy.html", []string{
			"Monto: $498.00", "Fecha: 20/07/2026", "Hora: 21:16",
		}},
		{"inflow.html", []string{
			"Monto: $299.00", "Fecha: 18 AGO 2026", "Hora: 18:54",
		}},
		{"service_payment.html", []string{
			"Monto: $909.00", "Tipo de transacción: Pago de servicio",
		}},
	}

	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			got := textOf(t, tt.file)
			lines := map[string]bool{}
			for _, l := range strings.Split(got, "\n") {
				lines[l] = true
			}
			for _, want := range tt.lines {
				if !lines[want] {
					t.Errorf("no line %q in extracted text", want)
				}
			}
			if strings.Contains(got, "font-size") || strings.Contains(got, "!important") {
				t.Error("CSS leaked into the extracted text")
			}
			if strings.Contains(got, "https://") {
				t.Error("a URL leaked into the extracted text")
			}
		})
	}
}

// The card payment template has no labels at all: the amount stands alone.
func TestTextOnCardPayment(t *testing.T) {
	got := textOf(t, "card_payment.html")
	if !strings.Contains(got, "$1,120.25") {
		t.Error("amount missing from card payment text")
	}
	if strings.Contains(got, "CHANGE FORMATTING") {
		t.Error("a comment leaked into the extracted text")
	}
}

func textOf(t *testing.T, file string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	got, err := parser.Text(raw)
	if err != nil {
		t.Fatalf("Text: %v", err)
	}
	return got
}
