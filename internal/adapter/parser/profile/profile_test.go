package profile_test

import (
	"errors"
	"testing"

	"github.com/alejandroayalad/billycore/internal/adapter/parser/profile"
	"github.com/alejandroayalad/billycore/internal/app"
)

// The email profile is the one with a parser today. It reads the 1,044 stored
// artifacts, and it is the profile migration 006 backfilled them with.
func TestTheEmailProfileSelectsTheNuParser(t *testing.T) {
	interpreter, err := profile.New().Select(profile.NuEmailV1, "message/rfc822", []byte("From: nu@nu.com.mx"))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if interpreter == nil {
		t.Fatal("Select returned no interpreter and no error")
	}
}

// The Nu statement profile now has a parser. It reads a PDF, so the signature
// selects it and nothing else does.
func TestTheNuStatementProfileSelectsTheStatementParser(t *testing.T) {
	interpreter, err := profile.New().Select(profile.NuStatementV1, "application/pdf", []byte("%PDF-1.7\n1 0 obj"))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if interpreter == nil {
		t.Fatal("Select returned no interpreter and no error")
	}
}

// A media type carries parameters, and they say nothing about which parser
// reads the artifact.
func TestSelectIgnoresContentTypeParameters(t *testing.T) {
	if _, err := profile.New().Select(profile.NuEmailV1, "Message/RFC822; charset=utf-8", nil); err != nil {
		t.Errorf("Select: %v", err)
	}
}

func TestSelectRejectsWhatTheProfileDoesNotExpect(t *testing.T) {
	cases := map[string]struct {
		profile     app.ExtractionProfile
		contentType string
		raw         []byte
		want        error
	}{
		// A PDF that arrived as an email, or an email that arrived as a PDF,
		// is a Source that is not what it says it is.
		"an email profile reading a pdf": {
			profile: profile.NuEmailV1, contentType: "application/pdf",
			raw: []byte("%PDF-1.7"), want: app.ErrSignatureMismatch,
		},
		// The content type is what the upload claimed. The bytes are the
		// artifact, and they are what BillyCore believes.
		"a statement that is not a pdf": {
			profile: profile.NuStatementV1, contentType: "application/pdf",
			raw: []byte("Estado de cuenta"), want: app.ErrSignatureMismatch,
		},
		"a profile BillyCore does not support": {
			profile: app.ExtractionProfile("NU_EMAIL_V2"), contentType: "message/rfc822",
			raw: []byte("From: nu@nu.com.mx"), want: app.ErrUnknownProfile,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := profile.New().Select(tc.profile, tc.contentType, tc.raw)
			if !errors.Is(err, tc.want) {
				t.Errorf("Select returned %v, want %v", err, tc.want)
			}
		})
	}
}

// The signature is validated before the parser. A wrong file reports what it
// is, and not what BillyCore cannot do with it.
func TestTheKlarStatementProfileSelectsTheStatementParser(t *testing.T) {
	interpreter, err := profile.New().Select(profile.KlarStatementV1, "application/pdf", []byte("%PDF-1.7\n1 0 obj"))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if interpreter == nil {
		t.Fatal("Select returned no interpreter and no error")
	}
}

func TestTheHSBCStatementProfileSelectsTheStatementParser(t *testing.T) {
	interpreter, err := profile.New().Select(profile.HSBCStatementV1, "application/pdf", []byte("%PDF-1.7\n1 0 obj"))
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if interpreter == nil {
		t.Fatal("Select returned no interpreter and no error")
	}
}

func TestTheSignatureIsCheckedBeforeTheParser(t *testing.T) {
	_, err := profile.New().Select(profile.HSBCStatementV1, "image/jpeg", []byte("\xff\xd8\xff"))
	if !errors.Is(err, app.ErrSignatureMismatch) {
		t.Errorf("Select returned %v, want %v", err, app.ErrSignatureMismatch)
	}
}

// Configuration validates a name against this list, so a name that Names
// reports and Known denies is a Source that cannot start (D26).
func TestEveryNameIsKnown(t *testing.T) {
	names := profile.Names()
	if len(names) != 4 {
		t.Errorf("Names returned %v, want the four supported profiles", names)
	}
	for _, name := range names {
		if !profile.Known(app.ExtractionProfile(name)) {
			t.Errorf("Names reports %q and Known denies it", name)
		}
	}
	if profile.Known("NU_EMAIL_V2") {
		t.Error("Known accepted a profile that is not registered")
	}
}
