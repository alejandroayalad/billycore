// Package gmail is the Gmail Source adapter: OAuth, fetching, and nothing else.
//
// It produces raw artifacts. It does not parse them, does not interpret them,
// and does not know what Evidence is. DOMAIN.md §9 — email connectivity is
// infrastructure, not domain logic.
package gmail

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CredentialsFile is the name of the file beside billy.db. D14 / SECURITY.md §5
// keep credentials out of the database so that the recommended backup gesture —
// copy billy.db — cannot carry a live refresh token with it.
const CredentialsFile = "credentials.json"

// Credentials is the whole file: OAuth secrets and the token they earned.
//
// Deliberately credentials only. Source *configuration* — which account, which
// query, which sender — is not stored here, because where it belongs is still an
// open question (CONTEXT.md §5 item 3) and D21 forbids closing one in passing.
type Credentials struct {
	Gmail *GmailCredentials `json:"gmail,omitempty"`
}

type GmailCredentials struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	RefreshToken string    `json:"refresh_token"`
	AccessToken  string    `json:"access_token,omitempty"`
	TokenExpiry  time.Time `json:"token_expiry,omitempty"`
}

// LoadCredentials reads the credential file, refusing one whose permissions say
// it has already leaked. A file that *is* the credential must not be silently
// usable when it is group- or world-readable (SECURITY.md §5).
//
// A missing file is not an error: it is the state before `billycore auth` runs.
func LoadCredentials(dir string) (*Credentials, error) {
	path := filepath.Join(dir, CredentialsFile)
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return &Credentials{}, nil
	}
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("%s is mode %04o: refusing to read a credential file others can read (want 0600)", path, perm)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		// Never include the file contents in the error. SECURITY.md §10.
		return nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return &c, nil
}

// Save writes the credential file at 0600, atomically.
//
// Temp file plus rename: a crash midway through a direct write leaves a
// truncated file, and a truncated credential file means re-running the whole
// interactive grant.
func (c *Credentials) Save(dir string) error {
	path := filepath.Join(dir, CredentialsFile)
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeds

	// Chmod before writing: the secret must never exist on disk world-readable,
	// not even for the microseconds between write and chmod.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
