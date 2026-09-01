# BillyCore — Agent Instructions

Entry point for anyone working in this repository, human or agent. `CLAUDE.md` points
here so there is one set of rules rather than two that drift.

Items marked **°** are defaults assumed on 2026-08-23 and not yet confirmed by the
author. They are listed together in §10 — correcting one is a sentence, not a redesign.

---

## 1. What this is

BillyCore reconstructs structured financial information from scattered sources — email
first, bank statements next. It is infrastructure, not an app, not an agent, and not a
budgeting UI. Those are BillySat and BillyAgent, and neither exists yet.

**There is a skeleton and no pipeline.** A `go.mod`, a binary that serves `/healthz`,
`Money`, `Currency`, and the D6 import-graph assertion — roughly 180 lines, and **no
commits**. No database, no Gmail, no Evidence, no `/v1` route. Everything in `docs/` is
unvalidated design that has never met a real email. Read
[ai/CONTEXT.md](ai/CONTEXT.md) before anything else — it is the only file that describes
where the project actually stands.

**The deadline is 2026-09-22 and it is hard.** The remaining budget is roughly 78 hours.
Nothing that does not serve this sentence gets built before then:

> I can see my last month of transactions, well classified, in a good table with good
> financial information.

---

## 2. Read order

| # | File | For |
|---|---|---|
| 1 | [ai/CONTEXT.md](ai/CONTEXT.md) | Where the project is. Always first. |
| 2 | [docs/PRODUCT.md](docs/PRODUCT.md) | What BillyCore is and refuses to be |
| 3 | [docs/DOMAIN.md](docs/DOMAIN.md) | Evidence, Claims, Transactions, Money, confidence |
| 4 | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | How it is built and why |
| 5 | [docs/DATA_MODEL.md](docs/DATA_MODEL.md) | Schema |
| 6 | [docs/API.md](docs/API.md) | The `/v1` contract |
| 7 | [docs/SECURITY.md](docs/SECURITY.md) | Threat model and the rules it imposes |
| 8 | [ai/DECISIONS.md](ai/DECISIONS.md) | What was decided, when, and what lost |

For a small task, 1 plus whichever of 3–7 the task touches. Do not skip 1.

---

## 3. Rules that do not bend

Violating one of these is not a style disagreement — it breaks a property the design
depends on.

1. **`internal/domain` imports nothing that performs I/O.** No `net/http`, no
   `database/sql`, no driver, no logger that writes. Enforced by a CI import-graph
   assertion. (D6)
2. **Money is `amount_minor INTEGER` plus `currency TEXT`.** Never a float. Never signed —
   direction is its own column. (DOMAIN.md §3)
3. **Evidence is immutable.** Persisted before extraction, never edited, never
   overwritten by a re-fetch. (D7, D10)
4. **No Claim without provenance** to existing Evidence. (DOMAIN.md §4)
5. **Invariants live in domain constructors**, not in HTTP handlers and not in database
   constraints. One place answers "is this valid?" whether the Claim came from a parser
   or over the wire. (D6, D11)
6. **Never log `raw_content`, credentials, or the bearer token.** Not at debug level, not
   temporarily. (SECURITY.md §10)
7. **Evidence is hostile input.** Never execute or render it, never resolve remote
   references, always bound size and time. (D16)
8. **No secrets in git, ever.** `billy.db`, `*.db-wal`, `*.db-shm`, and
   `credentials.json` are gitignored before the first commit, not after the first scare.
9. **No LLM call inside BillyCore.** AI proposes Claims through `POST /v1/claims`; Core
   validates. There is no `Extractor` port with a model adapter behind it. (D11)

---

## 4. Working modes

State the mode at the start of a session.

| Mode | |
|---|---|
| **Mode 1 — the author drives** | The author writes the code. The agent reviews, writes small pieces, fills placeholders. |
| **Mode 2 — the agent drives** | The agent writes. The author reviews. |

In Mode 2, **stop and show your work after one vertical slice or ~150 lines, whichever
comes first.°** At two hours a day, a 600-line drop that cannot be reviewed is worse than
no code at all.

---

## 5. Permissions

**Do without asking:** read anything · run tests · `go build`, `go vet`, `gofmt` ·
create branches · write scratch files outside the repo.

**Ask first:**

| Action | Why |
|---|---|
| `git commit`, `git push` | The author decides what enters history° |
| Adding a dependency to `go.mod` | Anything linked in gets full access to every secret the process holds (SECURITY.md §11) |
| Editing anything in `docs/` | The docs are the contract. Propose; the author edits.° |
| Editing an existing `DECISIONS.md` entry | Decisions are superseded by new entries, never rewritten |
| Touching `billy.db` or `credentials.json` | Real data, real credentials |
| Creating files in `billysat/` or `billyagent/` | Hard stop until BillyCore ships° |

---

## 6. Open questions

Around thirty are open across `docs/`, deliberately — those documents refuse to invent
answers no requirement has forced.

**When work reaches one: stop, ask, and log the answer in
[ai/DECISIONS.md](ai/DECISIONS.md).** Do not pick silently. Do not code around it. A
decision made in passing inside an implementation is the exact thing these documents
exist to prevent. (D21)

`DECISIONS.md` §"Awaiting a decision" lists the ten that matter, sorted by what they
block. The one that blocked the first slice — where `billy.db` lives (ARCHITECTURE.md
Q1) — is closed by D22. The five that block **M1** are listed in
[ai/CONTEXT.md](ai/CONTEXT.md) §5.

---

## 7. Commands

```bash
make check    # gofmt -l, go vet ./..., go test ./...
```

That is the check to run before showing work.° `gofmt` is not optional. `make build`
produces the binary; `make run` builds and starts it, which needs `BILLYCORE_TOKEN` set
or it refuses to start by design (SECURITY.md §4).

---

## 8. Code style

Standard Go. `gofmt`, standard library first, no ORM, no router framework unless one
earns its place (D5, D6).

- Tests from day one, not after the first slice works. The domain is pure specifically so
  it can be tested without infrastructure; unused, that property is worthless. (D19)
- Comments explain *why*, not *what*. The design documents carry the long reasoning —
  link to them rather than restating.
- English for code, comments, commits, and documentation.° The repository is going
  public.

**Comments follow [ASD-STE100](https://www.asd-ste100.org/) and are at most five lines.**
Simplified Technical English: short sentences, active voice, present tense, one idea per
sentence, and the same word for the same thing every time. No metaphor, no argument with
itself, no aside. If the reasoning needs more than five lines, it belongs in `docs/` or
in a `DECISIONS.md` entry, and the comment names that entry instead.

The rule exists because the reader may not be a native English speaker, and because a
comment that has to be read twice is a comment that will be skipped. §9's tone governs
the documents; this governs the code.

**Commit messages:** Conventional Commits — `feat:`, `fix:`, `docs:`, `test:`, `chore:`.°
Branch naming is unconstrained while the project is solo.°

---

## 9. Tone

The documents in this repository argue with themselves — *"defensible under hexagonal
architecture, and still the wrong call."* That is deliberate, and it is the house style.

**Disagree before writing the code, not after.°** An agent that thinks a decision is
wrong should say so, name the trade-off, and then either be overruled or change the
decision through `DECISIONS.md`. Silent compliance followed by a comment explaining the
regret is the worst of both.

Prefer the honest smaller claim. BillyCore may be uncertain, incomplete, or wrong — it
may never be unsupported.

---

## 10. Assumed defaults

Set on 2026-08-23 without confirmation. Each is cheap to change.

| ° | Assumption |
|---|---|
| §4 | Mode 2 stops at one vertical slice or ~150 lines |
| §5 | Commits and pushes are author-only |
| §5 | `docs/` edits are proposed, not made, by agents |
| §5 | `billysat/` and `billyagent/` are untouchable until BillyCore ships |
| §7 | `go build && go vet && go test` is the check command |
| §8 | English everywhere in the repository |
| §8 | ASD-STE100 comments, five lines maximum |
| §8 | Conventional Commits |
| §8 | Branch naming unconstrained |
| §9 | Agents push back before implementing, not after |
