// Package profile holds the extraction profiles that BillyCore supports.
//
// A profile is one reading contract: the format that a Source gives, and the
// parser that reads it. The vocabulary is closed, for the reason D50 closed the
// currencies. A known profile may precede its parser; D52 keeps its Evidence
// retryable and reports that state by name.
package profile

import (
	"bytes"
	"sort"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/extractor/pdftotext"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/nu"
	"github.com/alejandroayalad/billycore/internal/adapter/parser/nustatement"
	"github.com/alejandroayalad/billycore/internal/app"
)

// The supported profiles.
//
// The name is `<ISSUER>_<DOCUMENT FAMILY>_V<n>`, and the version is BillyCore's
// reading contract and not the issuer's layout. Nu changed its outflow email on
// 2026-07-22 and package nu absorbs both variants under one name. A new version
// is for a reading that changes in a way the old one cannot hold.
const (
	NuEmailV1       app.ExtractionProfile = "NU_EMAIL_V1"
	NuStatementV1   app.ExtractionProfile = "NU_STATEMENT_V1"
	HSBCStatementV1 app.ExtractionProfile = "HSBC_STATEMENT_V1"
)

// pdfMagic is the first bytes of a PDF file. A statement that does not start
// with it is not a PDF, whatever the upload called it.
var pdfMagic = []byte("%PDF-")

// entry is one profile: the formats it accepts, and the parser that reads it.
type entry struct {
	// contentTypes are the media types this profile accepts. The registry
	// compares the media type and ignores the parameters.
	contentTypes []string

	// magic is the signature at the start of the format, or nil where the
	// format has none. An email has no signature; a PDF has "%PDF-".
	magic []byte

	// interpreter is nil while the profile has no parser. The name is valid in
	// configuration, and extraction of such an artifact fails with a reason
	// that says so (D51 — the statement parser is its own slice).
	interpreter app.Interpreter
}

var registered = map[app.ExtractionProfile]entry{
	NuEmailV1: {
		contentTypes: []string{"message/rfc822"},
		interpreter:  nu.Interpreter{},
	},
	NuStatementV1: {
		contentTypes: []string{"application/pdf"},
		magic:        pdfMagic,
		// The parser holds the extractor, and the extractor resolves pdftotext
		// when it runs. A host with no Poppler starts, and each statement fails
		// with a named reason (D51).
		interpreter: nustatement.New(pdftotext.New()),
	},
	HSBCStatementV1: {
		contentTypes: []string{"application/pdf"},
		magic:        pdfMagic,
	},
}

// Registry selects the parser of an artifact. It implements
// app.InterpreterRegistry and it holds no state.
type Registry struct{}

func New() Registry { return Registry{} }

// Select validates the format signature and returns the parser of the profile.
//
// The signature is checked before the parser, so a wrong file reports what it
// is rather than what BillyCore cannot do with it. It reads the content type
// and the first bytes, and no more of the artifact than that.
func (Registry) Select(profile app.ExtractionProfile, contentType string, raw []byte) (app.Interpreter, error) {
	e, known := registered[profile]
	if !known {
		return nil, app.ErrUnknownProfile
	}
	if !accepts(e.contentTypes, contentType) {
		return nil, app.ErrSignatureMismatch
	}
	if len(e.magic) > 0 && !bytes.HasPrefix(raw, e.magic) {
		return nil, app.ErrSignatureMismatch
	}
	if e.interpreter == nil {
		return nil, app.ErrNoParserYet
	}
	return e.interpreter, nil
}

// Known reports if BillyCore supports the profile. Configuration uses it, so a
// name with a typo is a startup error and not a row that fails later (D26).
func Known(profile app.ExtractionProfile) bool {
	_, known := registered[profile]
	return known
}

// Names returns each supported profile, sorted. A configuration error lists
// them, so the reader learns the vocabulary from the message.
func Names() []string {
	names := make([]string, 0, len(registered))
	for name := range registered {
		names = append(names, string(name))
	}
	sort.Strings(names)
	return names
}

// accepts compares the media type and ignores the parameters and the case. A
// Source can report `text/plain; charset=utf-8`, and the parameters say nothing
// about which parser reads the artifact.
func accepts(contentTypes []string, contentType string) bool {
	media := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(media, ';'); i >= 0 {
		media = strings.TrimSpace(media[:i])
	}
	for _, accepted := range contentTypes {
		if media == accepted {
			return true
		}
	}
	return false
}

// Registry satisfies the port it was built for, checked at compile time.
var _ app.InterpreterRegistry = Registry{}
