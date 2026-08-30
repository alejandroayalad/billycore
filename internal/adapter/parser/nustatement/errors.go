package nustatement

import (
	"errors"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/app"
)

// The two failures of the reading path, named where they happen. The extractor
// and package bbox have their own errors, and this package does not import
// either sentinel set to tell one stage from the other.
var (
	errExtraction  = errors.New("nustatement: the statement could not be extracted")
	errCoordinates = errors.New("nustatement: the coordinate document could not be read")
)

// templateChanged is the phrase every drift error carries. It is a constant
// because classify matches on it, exactly as package nu does.
const templateChanged = "statement changed"

// interpretError carries a failure together with a rendering of it that holds
// no artifact.
//
// A parser error is not safe to store. ParseMoney and ParseWall render the text
// they choked on, and that text is a line of the statement. Error() keeps it
// for a reader in front of the document; Redacted() is what the pipeline stores
// (SECURITY.md §10).
type interpretError struct {
	class string
	err   error
}

func (e interpretError) Error() string    { return e.err.Error() }
func (e interpretError) Unwrap() error    { return e.err }
func (e interpretError) Redacted() string { return e.class }

// classify names the failure without repeating what caused it. It reads the
// identity of the error and never its text, so no part of the statement can
// travel with the reason.
func classify(err error) error {
	switch {
	case errors.Is(err, errExtraction):
		return interpretError{class: "the statement could not be extracted", err: err}
	case errors.Is(err, errCoordinates):
		return interpretError{class: "the coordinate document could not be read", err: err}
	case errors.Is(err, parser.ErrNotMoney), errors.Is(err, parser.ErrUnsupportedFmt):
		return interpretError{class: "the amount did not parse", err: err}
	case errors.Is(err, parser.ErrNotDate):
		return interpretError{class: "the date did not parse", err: err}
	case strings.Contains(err.Error(), templateChanged):
		return interpretError{class: "the statement changed", err: err}
	default:
		return interpretError{class: "the artifact could not be read", err: err}
	}
}

// interpretError describes itself safely, checked at compile time.
var _ app.Redacted = interpretError{}
