package gmail

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

const rfc822 = "From: nu@nu.com.mx\r\n" +
	"Date: Tue, 1 Jan 1980 00:00:00 +0000\r\n" +
	"Subject: Tu transferencia fue exitosa\r\n\r\n" +
	"Monto: $1,000.00\r\n"

func TestDecodeRawAcceptsBothPaddings(t *testing.T) {
	padded := base64.URLEncoding.EncodeToString([]byte(rfc822))
	unpadded := base64.RawURLEncoding.EncodeToString([]byte(rfc822))

	for name, encoded := range map[string]string{"padded": padded, "unpadded": unpadded} {
		t.Run(name, func(t *testing.T) {
			got, err := decodeRaw(encoded)
			if err != nil {
				t.Fatalf("decodeRaw: %v", err)
			}
			if string(got) != rfc822 {
				t.Error("the artifact did not survive decoding verbatim")
			}
		})
	}
}

// Gmail's encoding is web-safe: standard base64 would mangle '-' and '_'.
func TestDecodeRawHandlesWebSafeAlphabet(t *testing.T) {
	content := []byte{0xfb, 0xff, 0xbe, 0x00, 0x01, 0x02}
	encoded := base64.URLEncoding.EncodeToString(content)
	if !strings.ContainsAny(encoded, "-_") {
		t.Skip("this input no longer exercises the web-safe alphabet")
	}
	got, err := decodeRaw(encoded)
	if err != nil {
		t.Fatalf("decodeRaw: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("decodeRaw = % x, want % x", got, content)
	}
}

func TestDecodeRawRejectsGarbage(t *testing.T) {
	for _, s := range []string{"", "not base64 at all!!"} {
		if _, err := decodeRaw(s); err == nil {
			t.Errorf("decodeRaw(%q) accepted a payload it cannot decode", s)
		}
	}
}

// observed_at is the Source's receive time, never the artifact's own claim
// about itself (SECURITY.md §7, §8). The fixture above carries a Date: header
// from 1980 precisely so a regression would be obvious.
func TestArtifactObservedAtComesFromInternalDate(t *testing.T) {
	internalDate := time.Date(2026, 8, 16, 17, 44, 0, 0, time.UTC)
	message := &Message{ID: "18f2a9c4d5e6", InternalDate: internalDate}

	artifact := artifactFrom(message, []byte(rfc822))

	if !artifact.ObservedAt.Equal(internalDate) {
		t.Errorf("observed_at = %s, want internalDate %s", artifact.ObservedAt, internalDate)
	}
	if artifact.ObservedAt.Year() == 1980 {
		t.Error("observed_at came from the Date: header, which the sender controls")
	}
	if artifact.ContentType != ContentType {
		t.Errorf("content_type = %q, want %q", artifact.ContentType, ContentType)
	}
	if artifact.Reference != "18f2a9c4d5e6" {
		t.Errorf("reference = %q, want the Gmail message id", artifact.Reference)
	}
	if string(artifact.Content) != rfc822 {
		t.Error("the artifact was not carried through verbatim")
	}
}
