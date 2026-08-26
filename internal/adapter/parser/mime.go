package parser

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"strings"
)

// ErrNoHTMLPart reports an artifact with no text/html part to read.
//
// Measured 2026-08-25, every one of the 1,044 Nu artifacts is text/html and
// there is no text/plain alternative in any of them — so for this Source, no
// HTML part means no body at all.
var ErrNoHTMLPart = errors.New("parser: artifact has no text/html part")

// maxMIMEDepth bounds nested multipart recursion (SECURITY.md §7). Real mail
// nests two levels at most; six of the 1,044 artifacts are multipart/mixed
// wrapping a text/html part and PDF attachments, and nothing deeper exists.
const maxMIMEDepth = 4

// HTMLBody returns an artifact's text/html body as UTF-8.
//
// Attachments are ignored. They are bytes, never files: nothing here is
// written to a path, least of all one derived from a declared filename
// (SECURITY.md §7). The six PDF-bearing artifacts in the corpus are account
// contracts, not statements, and this function does not read them.
func HTMLBody(raw []byte) ([]byte, error) {
	if len(raw) > MaxInputBytes {
		return nil, ErrTooLarge
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parser: read message: %w", err)
	}
	return htmlFrom(textproto.MIMEHeader(msg.Header), msg.Body, 0)
}

// Header returns one decoded header value, or "" when it is absent.
// RFC 2047 encoded-words are decoded; a value that fails to decode is returned
// as it was written rather than replaced with a guess.
func Header(raw []byte, name string) (string, error) {
	if len(raw) > MaxInputBytes {
		return "", ErrTooLarge
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return "", fmt.Errorf("parser: read message: %w", err)
	}
	v := msg.Header.Get(name)
	if decoded, err := (&mime.WordDecoder{}).DecodeHeader(v); err == nil {
		return decoded, nil
	}
	return v, nil
}

func htmlFrom(header textproto.MIMEHeader, body io.Reader, depth int) ([]byte, error) {
	if depth > maxMIMEDepth {
		return nil, fmt.Errorf("parser: MIME nesting deeper than %d", maxMIMEDepth)
	}

	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil {
		// A missing or malformed Content-Type is text/plain by RFC 2045, which
		// for this Source means there is nothing to parse.
		return nil, ErrNoHTMLPart
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil, ErrNoHTMLPart
		}
		mr := multipart.NewReader(body, boundary)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				return nil, ErrNoHTMLPart
			}
			if err != nil {
				return nil, fmt.Errorf("parser: multipart: %w", err)
			}
			html, err := htmlFrom(part.Header, part, depth+1)
			part.Close()
			if err == nil {
				return html, nil
			}
			if !errors.Is(err, ErrNoHTMLPart) {
				return nil, err
			}
		}
	}

	if mediaType != "text/html" {
		return nil, ErrNoHTMLPart
	}

	decoded, err := decodeTransfer(header.Get("Content-Transfer-Encoding"), body)
	if err != nil {
		return nil, err
	}
	return toUTF8(decoded, params["charset"]), nil
}

func decodeTransfer(encoding string, body io.Reader) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64.NewDecoder(base64.StdEncoding, body)
	case "", "7bit", "8bit", "binary":
		// as-is
	default:
		return nil, fmt.Errorf("parser: unsupported transfer encoding %q", encoding)
	}
	b, err := io.ReadAll(io.LimitReader(body, MaxInputBytes))
	if err != nil {
		return nil, fmt.Errorf("parser: decode body: %w", err)
	}
	return b, nil
}

// toUTF8 transcodes a declared charset.
//
// 164 of the 1,044 artifacts declare iso-8859-1 and are genuinely encoded that
// way — 163 of them the legacy outflow template Nu used until 2026-07-21, all
// transaction-bearing. Read as UTF-8 they are invalid, and every accented
// label ("Número de referencia", "Tarjeta de débito") arrives corrupted, which
// would silently defeat any parser anchored on one.
//
// Latin-1 needs no dependency: every byte is its own code point, so the
// conversion is a range over bytes (SECURITY.md §11 — a transcoding library
// linked in here would get the same access to the process's secrets as
// everything else).
//
// windows-1252 is deliberately absent: it differs from latin-1 at 0x80-0x9F,
// so treating one as the other would be a guess. It does not appear in the
// corpus, and if it ever does the mapping table is a decision, not a default.
//
// An unrecognised charset is passed through as bytes rather than refused. Go
// treats invalid UTF-8 as replacement characters downstream, which is visible
// damage rather than a plausible wrong value.
func toUTF8(b []byte, charset string) []byte {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1":
		out := make([]byte, 0, len(b))
		for _, c := range b {
			out = append(out, []byte(string(rune(c)))...)
		}
		return out
	default:
		return b
	}
}
