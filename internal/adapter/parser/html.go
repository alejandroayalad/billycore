package parser

import (
	"errors"
	"strings"
)

// MaxInputBytes caps a single artifact's HTML body. SECURITY.md §7 requires
// every input bound to be explicit. The largest artifact in the corpus of
// 1,044 measured on 2026-08-25 is under 60 KiB; this leaves two orders of
// magnitude of headroom and still refuses a zip bomb's worth of markup.
const MaxInputBytes = 4 << 20

// ErrTooLarge reports an input above MaxInputBytes. The Evidence is untouched
// and the artifact can be re-parsed once the cap is raised deliberately.
var ErrTooLarge = errors.New("parser: input exceeds the size cap")

// discarded elements have their content thrown away along with their tags.
// Anything inside them is markup machinery or metadata, never a field value,
// and script text in particular must never reach a value parser.
var discarded = map[string]bool{
	"script":   true,
	"style":    true,
	"head":     true,
	"title":    true,
	"noscript": true,
}

// breaking elements end a line. Everything not listed is inline: <strong>,
// <span>, <a>, <b>, <em> and friends leave "Monto:" and its value on one line,
// which is what makes a label-anchored parser possible.
//
// Line *positions* are not stable across Nu's templates — only 35 of 345
// inflows share an identical line structure — so a parser anchors on the label
// and never on a line index. What this table has to guarantee is narrower: a
// label and its value stay together, and two unrelated cells never merge.
var breaking = map[string]bool{
	"br": true, "p": true, "div": true, "tr": true, "td": true, "th": true,
	"table": true, "tbody": true, "thead": true, "tfoot": true,
	"li": true, "ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"hr": true, "blockquote": true, "pre": true, "section": true,
	"article": true, "header": true, "footer": true, "body": true, "html": true,
}

// entities is the fixed table. Nothing outside it is expanded — no numeric
// references, no DTD, no external entity, ever (D16). Measured across all
// 1,044 artifacts, the corpus contains exactly three distinct entities:
// &nbsp; (87,499), &zwnj; (56,912) and &amp; (2). The rest of the table is the
// standard five, which cost nothing and would otherwise be a silent hole.
//
// An unrecognised entity is left verbatim rather than dropped or guessed at:
// "&euro;" reaching a value parser as literal text fails loudly, where a
// silently deleted one would leave a plausible-looking wrong number.
var entities = map[string]string{
	"&amp;":  "&",
	"&lt;":   "<",
	"&gt;":   ">",
	"&quot;": `"`,
	"&apos;": "'",
	"&nbsp;": " ",
	"&zwnj;": "",
}

// Text extracts readable text from an HTML email body.
//
// The result is newline-separated lines, each with its internal whitespace
// collapsed to single spaces and its edges trimmed; empty lines are dropped.
// Markup is discarded, not interpreted.
func Text(src []byte) (string, error) {
	if len(src) > MaxInputBytes {
		return "", ErrTooLarge
	}

	var out strings.Builder
	out.Grow(len(src) / 4)

	for i := 0; i < len(src); {
		if src[i] != '<' {
			j := i
			for j < len(src) && src[j] != '<' {
				j++
			}
			writeText(&out, src[i:j])
			i = j
			continue
		}

		// A comment, including the <!--[if mso]> conditional comments Nu's
		// templates are full of. Their contents are Outlook markup, and the
		// templates also hide editing instructions in them.
		if hasPrefixAt(src, i, "<!--") {
			if end := indexFrom(src, i+4, "-->"); end >= 0 {
				i = end + 3
			} else {
				i = len(src) // unterminated: discard the remainder
			}
			continue
		}

		name, after, closing, ok := scanTag(src, i)
		if !ok {
			// A '<' that begins no tag is ordinary text, and a doctype or
			// processing instruction is markup with nothing to say.
			if i+1 < len(src) && (src[i+1] == '!' || src[i+1] == '?') {
				if end := indexByteFrom(src, i, '>'); end >= 0 {
					i = end + 1
				} else {
					i = len(src)
				}
				continue
			}
			out.WriteByte('<')
			i++
			continue
		}

		if discarded[name] {
			out.WriteByte('\n')
			if closing {
				i = after
			} else {
				i = skipElement(src, after, name)
			}
			continue
		}
		if breaking[name] {
			out.WriteByte('\n')
		}
		i = after
	}

	return collapse(out.String()), nil
}

// writeText appends a run of character data, expanding the fixed entity table.
func writeText(out *strings.Builder, run []byte) {
	for i := 0; i < len(run); {
		if run[i] != '&' {
			// A newline inside character data is whitespace, not a line
			// break: only a tag ends a line. Otherwise a template that wrapped
			// its source between "Monto:" and the amount would split a label
			// from its value for no reason a reader of the email would see.
			if run[i] == '\n' || run[i] == '\r' {
				out.WriteByte(' ')
			} else {
				out.WriteByte(run[i])
			}
			i++
			continue
		}
		// An entity reference is short by construction; the longest in the
		// table is six bytes. Anything longer is not one.
		end := -1
		for j := i + 1; j < len(run) && j <= i+8; j++ {
			if run[j] == ';' {
				end = j
				break
			}
		}
		if end < 0 {
			out.WriteByte('&')
			i++
			continue
		}
		if v, known := entities[strings.ToLower(string(run[i:end+1]))]; known {
			out.WriteString(v)
			i = end + 1
			continue
		}
		out.WriteByte('&')
		i++
	}
}

// scanTag reads the tag starting at src[i] == '<'. It reports the lowercased
// element name, the index just past the closing '>', and whether the tag is a
// closing one. ok is false when what follows '<' is not an element name.
//
// Quoted attribute values are respected so that a '>' inside style="..." — a
// routine occurrence in these templates — does not end the tag early.
func scanTag(src []byte, i int) (name string, after int, closing, ok bool) {
	j := i + 1
	if j < len(src) && src[j] == '/' {
		closing = true
		j++
	}
	start := j
	for j < len(src) && isTagNameByte(src[j]) {
		j++
	}
	if j == start {
		return "", 0, false, false
	}
	name = strings.ToLower(string(src[start:j]))

	var quote byte
	for ; j < len(src); j++ {
		c := src[j]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return name, j + 1, closing, true
		}
	}
	return name, len(src), closing, true // unterminated tag: consume the rest
}

// skipElement discards everything up to and including the matching close tag.
// An unterminated element swallows the remainder, which is the safe direction:
// unclosed <style> means CSS, not text.
func skipElement(src []byte, from int, name string) int {
	closer := "</" + name
	for i := from; i < len(src); {
		k := indexByteFrom(src, i, '<')
		if k < 0 {
			return len(src)
		}
		if hasPrefixFoldAt(src, k, closer) {
			n := k + len(closer)
			if n >= len(src) || !isTagNameByte(src[n]) {
				if end := indexByteFrom(src, k, '>'); end >= 0 {
					return end + 1
				}
				return len(src)
			}
		}
		i = k + 1
	}
	return len(src)
}

// collapse normalises whitespace: every run within a line becomes one space,
// lines are trimmed, and empty lines are dropped.
//
// U+00A0 and U+200C are handled here as well as in the entity table, because
// Nu's templates emit both as literal UTF-8 as well as as references.
func collapse(s string) string {
	s = strings.ReplaceAll(s, "‌", "")
	s = strings.ReplaceAll(s, " ", " ")

	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func isTagNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func hasPrefixAt(src []byte, i int, prefix string) bool {
	return i+len(prefix) <= len(src) && string(src[i:i+len(prefix)]) == prefix
}

func hasPrefixFoldAt(src []byte, i int, prefix string) bool {
	return i+len(prefix) <= len(src) && strings.EqualFold(string(src[i:i+len(prefix)]), prefix)
}

func indexFrom(src []byte, i int, sub string) int {
	if i >= len(src) {
		return -1
	}
	k := strings.Index(string(src[i:]), sub)
	if k < 0 {
		return -1
	}
	return i + k
}

func indexByteFrom(src []byte, i int, c byte) int {
	for ; i < len(src); i++ {
		if src[i] == c {
			return i
		}
	}
	return -1
}
