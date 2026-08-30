#!/usr/bin/env bash
#
# session-digest — record what a working session actually changed.
#
# Wired to the SessionEnd hook in .codex/hooks.json. It writes
# ai/sessions/<date>.md and touches nothing else.
#
# It records *facts only*: what was committed, what changed, which decisions
# were logged, whether the tree is clean and green. It deliberately does not
# write prose and does not edit ai/CONTEXT.md or anything in docs/ — AGENTS.md
# §5 makes those the author's to edit, and a hook that rewrote the contract
# unreviewed is the thing that rule exists to prevent. What it can do is tell
# the author the contract has drifted, and hand them the facts to correct it
# with.
#
# Re-running on the same day overwrites that day's file rather than appending,
# so a session that ends twice does not produce a digest that double-counts.

set -uo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo" || exit 0

git rev-parse --git-dir >/dev/null 2>&1 || exit 0

branch="$(git branch --show-current 2>/dev/null || echo detached)"
today="$(date +%Y-%m-%d)"
out="ai/sessions/${today}.md"
mkdir -p ai/sessions

# The comparison point: the default branch where it exists, otherwise the root
# commit. A branch with no merge base still produces a digest rather than an error.
base="$(git merge-base main HEAD 2>/dev/null || git rev-list --max-parents=0 HEAD 2>/dev/null | head -1)"
range="${base}..HEAD"

commits="$(git log --oneline "$range" 2>/dev/null | wc -l | tr -d ' ')"

# gofmt and vet are seconds. `go test` without -count=1 uses the build cache, so
# an unchanged tree answers almost instantly — the point is to record the state,
# not to re-prove it.
check="not a Go module"
if [ -f go.mod ] && command -v go >/dev/null 2>&1; then
  fmt_dirty="$(gofmt -l . 2>/dev/null)"
  if [ -n "$fmt_dirty" ]; then
    check="FAIL — gofmt: $(echo "$fmt_dirty" | tr '\n' ' ')"
  elif ! go vet ./... >/dev/null 2>&1; then
    check="FAIL — go vet"
  elif go test ./... >/dev/null 2>&1; then
    check="green — gofmt, vet, tests"
  else
    check="FAIL — go test"
  fi
fi

dirty="$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
if [ "$dirty" = "0" ]; then tree="clean"; else tree="$dirty uncommitted path(s)"; fi

# Staleness: how many commits have landed since each document was last touched.
# This is the number that says "the contract has drifted", and it is the whole
# reason this file exists.
stale_report() {
  local doc="$1"
  [ -f "$doc" ] || { printf '| `%s` | — | missing |\n' "$doc"; return; }
  local last behind
  last="$(git log -1 --format=%ad --date=short -- "$doc" 2>/dev/null)"
  [ -z "$last" ] && last="uncommitted"
  behind="$(git rev-list --count "$(git log -1 --format=%H -- "$doc" 2>/dev/null)"..HEAD 2>/dev/null || echo 0)"
  if [ "${behind:-0}" -gt 0 ]; then
    printf '| `%s` | %s | **%s commits behind** |\n' "$doc" "$last" "$behind"
  else
    printf '| `%s` | %s | current |\n' "$doc" "$last"
  fi
}

{
  printf '# Session — %s\n\n' "$today"
  printf 'Branch `%s` · %s commit(s) ahead of the comparison point · working tree %s\n\n' \
    "$branch" "$commits" "$tree"
  printf '`make check`: %s\n\n' "$check"

  printf '## Commits\n\n'
  if [ "$commits" = "0" ]; then
    printf '_None._\n\n'
  else
    git log --format='- `%h` %s' "$range" 2>/dev/null
    printf '\n'
  fi

  printf '## Files changed\n\n'
  if [ "$commits" = "0" ]; then
    printf '_None._\n\n'
  else
    printf '```\n'
    git diff --stat "$range" 2>/dev/null | tail -40
    printf '```\n\n'
  fi

  printf '## Decisions logged\n\n'
  added="$(git diff "$range" -- ai/DECISIONS.md 2>/dev/null | grep -E '^\+### D[0-9]+' | sed 's/^+### /- /')"
  if [ -n "$added" ]; then printf '%s\n\n' "$added"; else printf '_None._\n\n'; fi

  printf '## Schema\n\n'
  if [ -d internal/adapter/store/sqlite/migrations ]; then
    printf '```\n'
    ls -1 internal/adapter/store/sqlite/migrations/*.sql 2>/dev/null | xargs -n1 basename 2>/dev/null
    printf '```\n\n'
  else
    printf '_No migrations directory._\n\n'
  fi

  printf '## Documents — drift since last touched\n\n'
  printf '| Document | Last updated | State |\n|---|---|---|\n'
  for doc in ai/CONTEXT.md ai/DECISIONS.md docs/DOMAIN.md docs/DATA_MODEL.md \
             docs/ARCHITECTURE.md docs/SECURITY.md docs/API.md docs/PRODUCT.md AGENTS.md; do
    stale_report "$doc"
  done
  printf '\n'
  printf '> Facts only. Correcting these documents is the author'"'"'s (AGENTS.md §5).\n'
} > "$out"

# Surfaced in the UI as the session closes.
context_behind="$(git rev-list --count "$(git log -1 --format=%H -- ai/CONTEXT.md 2>/dev/null)"..HEAD 2>/dev/null || echo 0)"
if [ "${context_behind:-0}" -gt 0 ]; then
  msg="Session digest → ${out} · ai/CONTEXT.md is ${context_behind} commits behind"
else
  msg="Session digest → ${out}"
fi
printf '{"systemMessage": %s, "suppressOutput": true}\n' "$(printf '%s' "$msg" | sed 's/\\/\\\\/g; s/"/\\"/g; s/^/"/; s/$/"/')"
exit 0
