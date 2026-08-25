package gmail

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	// The state before `billycore auth` has ever run.
	c, err := LoadCredentials(t.TempDir())
	if err != nil {
		t.Fatalf("missing credential file reported as an error: %v", err)
	}
	if c.Gmail != nil {
		t.Fatal("credentials appeared from nowhere")
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	want := &GmailCredentials{
		ClientID:     "id.apps.googleusercontent.com",
		ClientSecret: "secret",
		RefreshToken: "refresh",
		AccessToken:  "access",
		TokenExpiry:  time.Now().Add(time.Hour).UTC().Truncate(time.Second),
	}
	if err := (&Credentials{Gmail: want}).Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadCredentials(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Gmail == nil || got.Gmail.RefreshToken != want.RefreshToken || got.Gmail.ClientID != want.ClientID {
		t.Fatalf("round trip lost data: %+v", got.Gmail)
	}
	if !got.Gmail.TokenExpiry.Equal(want.TokenExpiry) {
		t.Errorf("expiry %v, want %v", got.Gmail.TokenExpiry, want.TokenExpiry)
	}
}

func TestSaveIsAlways0600(t *testing.T) {
	dir := t.TempDir()
	if err := (&Credentials{Gmail: &GmailCredentials{RefreshToken: "r"}}).Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, CredentialsFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("credential file is mode %04o, want 0600 (SECURITY.md §5)", perm)
	}
}

func TestSaveLeavesNoTempFileBehind(t *testing.T) {
	dir := t.TempDir()
	if err := (&Credentials{Gmail: &GmailCredentials{RefreshToken: "r"}}).Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != CredentialsFile {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected only %s, found %v — a temp file holding a refresh token was left on disk", CredentialsFile, names)
	}
}

func TestLoadRefusesAWorldReadableFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, CredentialsFile)
	if err := os.WriteFile(path, []byte(`{"gmail":{"refresh_token":"r"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentials(dir); err == nil {
		t.Fatal("a 0644 credential file was read; its permissions say it has already leaked (SECURITY.md §5)")
	}
}

func TestLoadErrorNeverEchoesFileContents(t *testing.T) {
	dir := t.TempDir()
	const secret = "super-secret-refresh-token"
	if err := os.WriteFile(filepath.Join(dir, CredentialsFile), []byte(`{"gmail": `+secret), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadCredentials(dir)
	if err == nil {
		t.Fatal("malformed JSON accepted")
	}
	if contains(err.Error(), secret) {
		t.Fatalf("the error echoes the file contents: %v (SECURITY.md §10)", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

func TestPKCEChallengeIsDerivedFromTheVerifier(t *testing.T) {
	v1, c1, err := pkce()
	if err != nil {
		t.Fatal(err)
	}
	v2, c2, err := pkce()
	if err != nil {
		t.Fatal(err)
	}
	if v1 == v2 || c1 == c2 {
		t.Fatal("pkce() is not random; a predictable verifier defeats the point")
	}
	if v1 == c1 {
		t.Fatal("challenge equals verifier; it must be the S256 hash")
	}
}
