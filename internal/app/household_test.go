package app_test

import (
	"testing"

	"github.com/alejandroayalad/billycore/internal/app"
)

func TestAnEmptyHouseholdHasNoOwnedSources(t *testing.T) {
	if !(app.Household{}).Empty() {
		t.Error("zero Household is not Empty")
	}
	h := app.Household{AliasAccount: map[string]string{"klar": "klar"}}
	if !h.Empty() {
		t.Error("aliases without a Source binding must not create transfers")
	}
	h.SourceAccount = map[string]string{"klar_statements": "klar"}
	if h.Empty() {
		t.Error("a bound Source must make Household not Empty")
	}
}
