// Package bbox turns `pdftotext -bbox-layout` coordinates into positioned rows.
//
// A PDF holds words at coordinates and no rows. This package rebuilds the rows
// of a statement from the geometry, and reads nothing: no money, no date, no
// domain type. Interpretation stays in the statement parser above it (D54).
package bbox

import (
	"encoding/xml"
	"errors"
	"strings"
)

// MaxInputBytes caps one coordinate document. It is the output limit of the
// extractor, so a document that passed the process boundary fits here.
const MaxInputBytes = 16 << 20

// ErrTooLarge reports an input above MaxInputBytes.
var ErrTooLarge = errors.New("bbox: coordinate document exceeds the configured limit")

// Document is the coordinate XHTML of one artifact. The nesting is the one
// poppler emits: page, flow, block, line, word.
type Document struct {
	XMLName xml.Name `xml:"html"`
	Pages   []Page   `xml:"body>doc>page"`
}

type Page struct {
	Width  float64 `xml:"width,attr"`
	Height float64 `xml:"height,attr"`
	Blocks []Block `xml:"flow>block"`
}

type Block struct {
	XMin  float64 `xml:"xMin,attr"`
	YMin  float64 `xml:"yMin,attr"`
	XMax  float64 `xml:"xMax,attr"`
	YMax  float64 `xml:"yMax,attr"`
	Lines []Line  `xml:"line"`
}

type Line struct {
	XMin  float64 `xml:"xMin,attr"`
	YMin  float64 `xml:"yMin,attr"`
	XMax  float64 `xml:"xMax,attr"`
	YMax  float64 `xml:"yMax,attr"`
	Words []Word  `xml:"word"`
}

type Word struct {
	XMin float64 `xml:"xMin,attr"`
	YMin float64 `xml:"yMin,attr"`
	XMax float64 `xml:"xMax,attr"`
	YMax float64 `xml:"yMax,attr"`
	Text string  `xml:",chardata"`
}

// Text joins the words of the line with one space.
func (l Line) Text() string {
	words := make([]string, 0, len(l.Words))
	for _, w := range l.Words {
		if text := strings.TrimSpace(w.Text); text != "" {
			words = append(words, text)
		}
	}
	return strings.Join(words, " ")
}

// Parse reads a coordinate document with the standard library, which D54
// requires. encoding/xml expands no external entity and resolves no remote
// reference, so hostile input stays inert (D16).
func Parse(data []byte) (*Document, error) {
	if len(data) > MaxInputBytes {
		return nil, ErrTooLarge
	}
	var doc Document
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
