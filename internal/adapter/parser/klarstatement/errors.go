package klarstatement

import (
	"errors"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser"
	"github.com/alejandroayalad/billycore/internal/app"
)

var (
	errExtraction  = errors.New("klarstatement: the statement could not be extracted")
	errCoordinates = errors.New("klarstatement: the coordinate document could not be read")
)

const templateChanged = "statement changed"

type interpretError struct {
	class string
	err   error
}

func (e interpretError) Error() string    { return e.err.Error() }
func (e interpretError) Unwrap() error    { return e.err }
func (e interpretError) Redacted() string { return e.class }

func classify(err error) error {
	switch {
	case errors.Is(err, errExtraction):
		return interpretError{class: "the statement could not be extracted", err: err}
	case errors.Is(err, errCoordinates):
		return interpretError{class: "the coordinate document could not be read", err: err}
	case errors.Is(err, parser.ErrNotMoney), errors.Is(err, parser.ErrUnsupportedFmt):
		return interpretError{class: "the amount did not parse", err: err}
	case strings.Contains(err.Error(), templateChanged):
		return interpretError{class: "the statement changed", err: err}
	default:
		return interpretError{class: "the artifact could not be read", err: err}
	}
}

var _ app.Redacted = interpretError{}
