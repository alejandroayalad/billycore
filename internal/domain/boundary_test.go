package domain_test

import (
	"os/exec"
	"strings"
	"testing"
)

// allowed is every package internal/domain may import directly.
//
// Deny by default. Adding to this list is a design decision: the rule it
// protects is ARCHITECTURE.md §4 / DECISIONS.md D6 —
//
//	internal/domain imports nothing that performs I/O.
//
// Not "avoids I/O". Cannot reach it. No net/http, no database/sql, no driver,
// no os, no logger that writes anywhere.
var allowed = map[string]bool{
	"errors":  true,
	"fmt":     true,
	"math":    true,
	"sort":    true,
	"strconv": true,
	"strings": true,
	"time":    true,
	"unicode": true,
}

func TestDomainImportsNoIO(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}}\n{{end}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, imp := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		imp = strings.TrimSpace(imp)
		if imp == "" || allowed[imp] {
			continue
		}
		if strings.HasPrefix(imp, "github.com/alejandroayalad/billycore/internal/domain") {
			continue // the domain may be split into subpackages
		}
		t.Errorf("internal/domain imports %q — the domain performs no I/O (D6). "+
			"If this import is genuinely pure, add it to the allowlist deliberately.", imp)
	}
}
