// Package config reads the files BillyCore is configured with, other than its
// credentials — those live in their own file, at their own permissions (D14).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/alejandroayalad/billycore/internal/domain"
)

// SourcesFile is the name of the configuration file, read from the data
// directory beside billy.db (D22, D26).
const SourcesFile = "sources.json"

// Source is one configured Source: what to call it, what kind it is, and how to
// find its artifacts.
//
// It holds no credentials. A Source is configuration and a refresh token is a
// secret; they have different lifetimes and different blast radii, which is why
// D14 keeps credentials.json separate and 0600.
type Source struct {
	ID   string            `json:"id"`
	Type domain.SourceType `json:"type"`

	// Query is the Source's fetch configuration. For Gmail it is a search query
	// such as `from:nu@nu.com.mx`.
	Query string `json:"query"`
}

type sourcesDocument struct {
	Sources []Source `json:"sources"`
}

// LoadSources reads sources.json.
//
// A missing file is not an error: BillyCore starts with no Source configured,
// and `POST /v1/sources/{id}/sync` answers 404 for every id, which is exactly
// what API.md §5 specifies for an unconfigured Source. A file that exists and is
// wrong *is* an error — a typo that silently produced an empty set would surface
// as a 404 from sync, which is the least informative way possible to learn about
// it (D26).
func LoadSources(dir string) ([]Source, error) {
	path := filepath.Join(dir, SourcesFile)

	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}

	var document sourcesDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if err := validate(document.Sources); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return document.Sources, nil
}

func validate(sources []Source) error {
	seen := make(map[string]bool, len(sources))
	for i, s := range sources {
		if s.ID == "" {
			return fmt.Errorf("source %d has no id", i)
		}
		if seen[s.ID] {
			// Two Sources sharing an id means one of them is unreachable, and
			// worse, their Evidence would share an identity namespace (D10).
			return fmt.Errorf("source id %q appears twice", s.ID)
		}
		seen[s.ID] = true

		if err := s.Type.Validate(); err != nil {
			return fmt.Errorf("source %q: %w", s.ID, err)
		}
		if s.Type == domain.SourceGmail && s.Query == "" {
			// An empty Gmail query matches the entire mailbox. That is not a
			// configuration anyone means to write, and it is not a mistake to
			// discover after a thousand unrelated artifacts are recorded.
			return fmt.Errorf("source %q is GMAIL and has no query", s.ID)
		}
	}
	return nil
}
