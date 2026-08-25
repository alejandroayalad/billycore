package id

import (
	"regexp"
	"testing"
)

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIsAUUIDV4(t *testing.T) {
	got, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !uuidV4.MatchString(got) {
		t.Errorf("New() = %q, which is not a UUID v4", got)
	}
}

func TestNewDoesNotRepeat(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for range 1000 {
		got, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[got] {
			t.Fatalf("New() returned %q twice", got)
		}
		seen[got] = true
	}
}
