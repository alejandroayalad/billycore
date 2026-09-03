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
	"sort"
	"strings"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/profile"
	"github.com/alejandroayalad/billycore/internal/app"
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

	// ExtractionProfile names the reading contract of this Source's artifacts,
	// such as `NU_EMAIL_V1`. Ingestion copies it onto each Evidence row, and
	// extraction selects the parser from it. There is no default: a guess reads
	// a statement with an email parser.
	ExtractionProfile app.ExtractionProfile `json:"extraction_profile"`

	// OwnedAccount is the household account this Source posts (D78). A
	// BANK_STATEMENT Source must name one. Gmail may omit it.
	OwnedAccount string `json:"owned_account"`
}

// Account is one account the user owns (D78). Aliases are exact merchant or
// counterparty strings that name this account on another ledger.
type Account struct {
	ID      string   `json:"id"`
	Aliases []string `json:"aliases"`
}

type sourcesDocument struct {
	Accounts []Account `json:"accounts"`
	Sources  []Source  `json:"sources"`
}

// LoadFile reads sources.json: the Sources and the household they belong to.
func LoadFile(dir string) ([]Source, app.Household, error) {
	path := filepath.Join(dir, SourcesFile)

	content, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, app.Household{}, nil
	}
	if err != nil {
		return nil, app.Household{}, fmt.Errorf("cannot read %s: %w", path, err)
	}

	var document sourcesDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return nil, app.Household{}, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	household, err := validate(document)
	if err != nil {
		return nil, app.Household{}, fmt.Errorf("%s: %w", path, err)
	}
	return document.Sources, household, nil
}

// LoadSources reads sources.json. A missing file is not an error: BillyCore
// starts with no Source configured (D26).
func LoadSources(dir string) ([]Source, error) {
	sources, _, err := LoadFile(dir)
	return sources, err
}

// validateProfile holds the configuration to the closed vocabulary of
// supported profiles. A name with a typo stops the start, for the reason D26
// gives: the alternative is an artifact that fails one row at a time, days
// later, with nobody watching.
func validateProfile(s Source) error {
	if s.ExtractionProfile == "" {
		return fmt.Errorf("source %q has no extraction_profile: use one of %s",
			s.ID, strings.Join(profile.Names(), ", "))
	}
	if !profile.Known(s.ExtractionProfile) {
		return fmt.Errorf("source %q has extraction_profile %q, which BillyCore does not support: use one of %s",
			s.ID, string(s.ExtractionProfile), strings.Join(profile.Names(), ", "))
	}
	return nil
}

func validate(document sourcesDocument) (app.Household, error) {
	accounts := make(map[string]bool, len(document.Accounts))
	household := app.Household{
		SourceAccount: map[string]string{},
		AliasAccount:  map[string]string{},
	}
	for i, a := range document.Accounts {
		if a.ID == "" {
			return app.Household{}, fmt.Errorf("account %d has no id", i)
		}
		if accounts[a.ID] {
			return app.Household{}, fmt.Errorf("account id %q appears twice", a.ID)
		}
		accounts[a.ID] = true
		for _, alias := range a.Aliases {
			if alias == "" {
				return app.Household{}, fmt.Errorf("account %q has an empty alias", a.ID)
			}
			if other, exists := household.AliasAccount[alias]; exists {
				return app.Household{}, fmt.Errorf("alias %q points at %q and %q", alias, other, a.ID)
			}
			household.AliasAccount[alias] = a.ID
		}
	}

	seen := make(map[string]bool, len(document.Sources))
	for i, s := range document.Sources {
		if s.ID == "" {
			return app.Household{}, fmt.Errorf("source %d has no id", i)
		}
		if seen[s.ID] {
			return app.Household{}, fmt.Errorf("source id %q appears twice", s.ID)
		}
		seen[s.ID] = true

		if err := s.Type.Validate(); err != nil {
			return app.Household{}, fmt.Errorf("source %q: %w", s.ID, err)
		}
		if err := validateProfile(s); err != nil {
			return app.Household{}, err
		}
		if s.Type == domain.SourceGmail && s.Query == "" {
			return app.Household{}, fmt.Errorf("source %q is GMAIL and has no query", s.ID)
		}
		if s.Type == domain.SourceBankStatement && s.OwnedAccount == "" {
			return app.Household{}, fmt.Errorf("source %q is BANK_STATEMENT and has no owned_account", s.ID)
		}
		if s.OwnedAccount != "" {
			if !accounts[s.OwnedAccount] {
				ids := make([]string, 0, len(accounts))
				for id := range accounts {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				if len(ids) == 0 {
					return app.Household{}, fmt.Errorf("source %q names owned_account %q, and no account is configured",
						s.ID, s.OwnedAccount)
				}
				return app.Household{}, fmt.Errorf("source %q names owned_account %q, which is not configured: use one of %s",
					s.ID, s.OwnedAccount, strings.Join(ids, ", "))
			}
			household.SourceAccount[s.ID] = s.OwnedAccount
		}
	}
	return household, nil
}
