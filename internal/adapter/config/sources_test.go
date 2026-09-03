package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/profile"
	"github.com/alejandroayalad/billycore/internal/domain"
)

func writeSources(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, SourcesFile), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", SourcesFile, err)
	}
	return dir
}

func TestLoadSources(t *testing.T) {
	dir := writeSources(t, `{
	  "sources": [
	    { "id": "gmail_primary", "type": "GMAIL", "query": "from:nu@nu.com.mx",
	      "extraction_profile": "NU_EMAIL_V1" }
	  ]
	}`)

	sources, err := LoadSources(dir)
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("loaded %d sources, want 1", len(sources))
	}
	want := Source{
		ID:                "gmail_primary",
		Type:              domain.SourceGmail,
		Query:             "from:nu@nu.com.mx",
		ExtractionProfile: profile.NuEmailV1,
	}
	if sources[0] != want {
		t.Errorf("source = %+v, want %+v", sources[0], want)
	}
}

func TestLoadFileBindsStatementSourcesToOwnedAccounts(t *testing.T) {
	dir := writeSources(t, `{
	  "accounts": [
	    { "id": "flex", "aliases": ["ALEJANDRO DE JESUS AYALA DIAZ"] },
	    { "id": "nu" },
	    { "id": "klar", "aliases": ["klar"] }
	  ],
	  "sources": [
	    { "id": "hsbc_statements", "type": "BANK_STATEMENT",
	      "extraction_profile": "HSBC_STATEMENT_V1", "owned_account": "flex" },
	    { "id": "nu_statements", "type": "BANK_STATEMENT",
	      "extraction_profile": "NU_STATEMENT_V1", "owned_account": "nu" },
	    { "id": "klar_statements", "type": "BANK_STATEMENT",
	      "extraction_profile": "KLAR_STATEMENT_V1", "owned_account": "klar" }
	  ]
	}`)

	sources, household, err := LoadFile(dir)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(sources) != 3 {
		t.Fatalf("loaded %d sources, want 3", len(sources))
	}
	if household.SourceAccount["klar_statements"] != "klar" {
		t.Errorf("klar Source bound to %q", household.SourceAccount["klar_statements"])
	}
	if household.AliasAccount["klar"] != "klar" {
		t.Errorf("alias klar points at %q", household.AliasAccount["klar"])
	}
	if household.AliasAccount["ALEJANDRO DE JESUS AYALA DIAZ"] != "flex" {
		t.Errorf("ALEJANDRO alias points at %q", household.AliasAccount["ALEJANDRO DE JESUS AYALA DIAZ"])
	}
}

// No file means no Source configured, which sync reports as a 404. It is not a
// reason to refuse to start.
func TestLoadSourcesAcceptsAMissingFile(t *testing.T) {
	sources, err := LoadSources(t.TempDir())
	if err != nil {
		t.Fatalf("LoadSources: %v", err)
	}
	if len(sources) != 0 {
		t.Errorf("loaded %d sources from an empty directory", len(sources))
	}
}

func TestLoadSourcesRejectsBadConfiguration(t *testing.T) {
	cases := map[string]struct {
		content string
		says    string
	}{
		"malformed JSON": {
			content: `{"sources": [`,
			says:    "not valid JSON",
		},
		"no id": {
			content: `{"sources": [{"type": "GMAIL", "query": "from:nu@nu.com.mx", "extraction_profile": "NU_EMAIL_V1"}]}`,
			says:    "no id",
		},
		"duplicate id": {
			content: `{"sources": [
			  {"id": "gmail_primary", "type": "GMAIL", "query": "from:nu@nu.com.mx", "extraction_profile": "NU_EMAIL_V1"},
			  {"id": "gmail_primary", "type": "GMAIL", "query": "from:hsbc@hsbc.com.mx", "extraction_profile": "NU_EMAIL_V1"}
			]}`,
			says: "twice",
		},
		"unknown type": {
			content: `{"sources": [{"id": "whatsapp", "type": "WHATSAPP", "query": "x", "extraction_profile": "NU_EMAIL_V1"}]}`,
			says:    "not a known kind of Source",
		},
		"gmail without a query": {
			content: `{"sources": [{"id": "gmail_primary", "type": "GMAIL", "extraction_profile": "NU_EMAIL_V1"}]}`,
			says:    "no query",
		},
		// The profile names the parser. A Source with none records artifacts
		// that nothing reads, and a name with a typo does the same.
		"no extraction profile": {
			content: `{"sources": [{"id": "gmail_primary", "type": "GMAIL", "query": "from:nu@nu.com.mx"}]}`,
			says:    "no extraction_profile",
		},
		"unsupported extraction profile": {
			content: `{"sources": [{"id": "gmail_primary", "type": "GMAIL", "query": "from:nu@nu.com.mx",
			  "extraction_profile": "NU_EMAIL_V2"}]}`,
			says: "does not support",
		},
		"statement without an owned account": {
			content: `{"accounts": [{"id": "nu"}], "sources": [
			  {"id": "nu_statements", "type": "BANK_STATEMENT", "extraction_profile": "NU_STATEMENT_V1"}
			]}`,
			says: "no owned_account",
		},
		"owned account that does not exist": {
			content: `{"accounts": [{"id": "nu"}], "sources": [
			  {"id": "hsbc_statements", "type": "BANK_STATEMENT", "extraction_profile": "HSBC_STATEMENT_V1",
			   "owned_account": "flex"}
			]}`,
			says: "not configured",
		},
		"duplicate alias": {
			content: `{"accounts": [
			  {"id": "flex", "aliases": ["klar"]},
			  {"id": "klar", "aliases": ["klar"]}
			], "sources": []}`,
			says: "points at",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadSources(writeSources(t, tc.content))
			if err == nil {
				t.Fatal("LoadSources accepted a configuration it cannot act on")
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error does not explain the problem: %v", err)
			}
		})
	}
}
