# BillyCore — Context

**Last updated:** 2026-08-24

Orientation for anyone — human or agent — starting work on BillyCore. `docs/` describes
the system as designed. This file describes where the project actually *is*, which is a
different and much shorter story.

Read this first, then [docs/PRODUCT.md](../docs/PRODUCT.md).

---

## 1. State: a skeleton exists, the pipeline does not

There is a `go.mod`, a binary that runs, and roughly 180 lines of Go. There is still
**no commit**.

What runs today: `billycore` starts, creates `~/.billy` at `0700`, refuses to start
without a bearer token, warns when bound off-loopback, serves `GET /healthz`, and shuts
down on a signal. Alongside it, `Money` and `Currency` exist as pure domain values, and
the import-graph assertion that enforces D6 is written and passing.

What does not exist: no database, no migration, no Gmail, no Evidence, no Claim, no
Transaction, no `/v1` route of any kind. **The entire pipeline described in `docs/` is
unwritten.** The ratio of design prose to code is roughly 600:1.

Everything in `docs/` is **unvalidated design**. It has never been run against a real
email. Treat it as a well-reasoned hypothesis, not as a description of working software.

### Code

| Path | Lines | State |
|---|---|---|
| `go.mod` | — | Go 1.26.2, **zero dependencies** |
| `cmd/billycore/main.go` | ~140 | Serves `/healthz`; data dir, token, and bind-address checks |
| `internal/domain/money.go` | 58 | Integer minor units; rejects negatives and mixed-currency addition |
| `internal/domain/currency.go` | 25 | Shape validation only — no minor-unit exponent (API.md Q8) |
| `internal/domain/boundary_test.go` | 45 | The D6 import-graph assertion |
| `internal/domain/money_test.go` | 54 | Passing |
| `Makefile` | — | `make check` = fmt + vet + test |
| `.gitignore` | — | `billy.db*`, `credentials.json`, build output |

### Documents

| Document | State |
|---|---|
| `docs/PRODUCT.md` | Written |
| `docs/ARCHITECTURE.md` | Written |
| `docs/DOMAIN.md` | Written |
| `docs/DATA_MODEL.md` | Written |
| `docs/API.md` | Written |
| `docs/SECURITY.md` | Written |
| `docs/ROADMAP.md` | **Empty** |
| `ai/CONTEXT.md`, `ai/DECISIONS.md` | Written |
| `ai/CONVENTIONS.md`, `ai/CONSTRAINTS.md`, `ai/WORKFLOW.md`, `ai/GLOSSARY.md` | **Empty** |
| BillySat, BillyAgent | Docs-only scaffolds, no code. Do not start them. |

---

## 2. The clock

| | |
|---|---|
| Started | 2026-08-22 |
| MVP deadline | **2026-09-22 — hard** |
| Budget | 2 h/weekday + 4 h Saturday + 4 h Sunday ≈ **18 h/week** |
| Total remaining | **≈ 78 hours** |

That number is the most important fact in this document. Every design decision in
`docs/` is subordinate to it. Seventy-eight hours is roughly two normal working weeks,
spent in two-hour fragments, on a system with six design documents and around thirty
open questions.

The success criterion has not moved (PRODUCT.md):

> I can see my last month of transactions, well classified, in a good table with good
> financial information.

**Nothing that does not serve that sentence should be built before 2026-09-22.**

---

## 3. Next step

**Gmail OAuth flow.** Get real email into the system.

This is the right first move, and the reason is §1: no part of this design has met real
data. The two banks that matter are **Nubank** and **HSBC**, and until their actual
emails are in hand, every parser decision is speculation.

Rough shape of the first vertical slice, in order:

1. ~~`go mod init`, one binary that starts and serves `/healthz`~~ — **done**
2. SQLite open, embedded migration, `evidence` table only
3. Gmail OAuth — read-only scopes (SECURITY.md §5), token to `credentials.json` at `0600`
4. Fetch → persist Evidence at stage `RECEIVED`, stop there
5. **Look at real Nubank and HSBC emails.** Then design the parser.

Steps 2–4 are milestone **M1**, scoped to Gmail → Evidence only: no Claims, no
transaction extraction, no reconciliation, no AI, no HSBC. Nu (`nu@nu.com.mx`) is the
only sender in scope, the Gmail message id is the source artifact key, and sync must be
idempotent.

Step 5 is where the design meets reality and where `docs/` starts getting corrected.

---

## 4. Decisions that blocked step 1 — both closed

Both questions that blocked the first slice were answered before it was written, and
step 1 has since shipped. They are kept here because M1 depends on both.

- **ARCHITECTURE.md Q1** — where `billy.db` lives. **Closed** by D22: `~/.billy/billy.db`,
  in a `0700` directory BillyCore creates at startup.
- **ARCHITECTURE.md Q2** — credential storage. **Closed** by SECURITY.md §5 / D14: a
  separate `credentials.json`, `0600`, beside the database — now `~/.billy/credentials.json`.

These do *not* block step 1 and must not be argued about yet:

- DATA_MODEL.md Q1 — what a reconciliation candidate references. Blocks reconciliation,
  which is week three at the earliest.
- API.md Q8 — the currency minor-unit exponent. Blocks rendering amounts, not storing
  them.
- Everything in DOMAIN.md §10.

---

## 5. Open questions in this repo

There are around thirty across the six documents, and they are deliberate — the docs
refuse to invent answers that no requirement has forced yet.

**Protocol when work hits one:** stop, ask, and log the answer in `ai/DECISIONS.md`.
Do not pick silently, and do not code around it. A decision made in passing inside an
implementation is exactly what these documents exist to prevent.

### Found while scoping M1 — 2026-08-24

Five open items that reading the documents together surfaced. None is answered here.
They are recorded so M1 does not close one by accident; each needs an author decision
and a `DECISIONS.md` entry.

1. **`source_artifact_key` vs `source_reference` — three names for one concept.**
   ARCHITECTURE.md §5 and the API wire field say `source_artifact_key`;
   DATA_MODEL.md §4.1's column is `source_reference`, where it *also* carries a second
   meaning — "the preserved reference when `raw_content` is `NULL`". For Gmail both are
   the message id, so M1 will not notice. The domain constructor has to name one, and
   whichever it names becomes canonical by default.
2. **`content_type` is on the wire and not in the schema.** API.md §4 and §6 carry
   `content_type` and `content_bytes` on Evidence; the `evidence` table has neither
   column. `content_bytes` is derivable; `content_type` is not. `GET /v1/evidence/{id}`
   cannot match the documented shape without a schema change or a contract change.
3. **Where does Source *configuration* live?** `POST /v1/sources/{id}/sync` returns
   `404` for an unconfigured Source, but D13 says there is no Source table and D14
   scoped `credentials.json` to credentials only. Nothing says where "`gmail_primary`
   means this account, filtered to `from:nu@nu.com.mx`" is written down. M1 needs it.
4. **The OAuth grant is interactive; the daemon is not.** SECURITY.md §6 argues against
   anything that turns BillyCore into a binary you babysit through every restart. The
   first grant needs a browser and a loopback callback, and `main.go` has no subcommand
   structure. A `billycore auth` subcommand is the obvious shape and no document
   describes one.
5. **Is `EvidenceIngested` emitted in M1?** DATA_MODEL.md §4.7 requires an event to be
   written in the same transaction as the change producing it; DOMAIN.md Q10 asks which
   events v1 actually needs. Skipping it leaves the first Evidence rows permanently
   without an event. Including it pulls `domain_event` into migration 001. This one gets
   more expensive the longer it waits.

---

## 6. How to work here

Two modes, and the mode is stated at the start of a session:

| Mode | Meaning |
|---|---|
| **Mode 1 — I drive** | The human writes the code. The agent reviews, writes small pieces, and fills in placeholders. |
| **Mode 2 — Agent drives** | The agent writes the code. The human reviews. |

Neither mode changes the standards below.

**Tests from day one.** Not after the first slice works. The domain layer is pure Go with
no I/O (ARCHITECTURE.md §4) specifically so it can be tested without infrastructure —
that property is worthless if it goes unused. The import-graph assertion in
ARCHITECTURE.md §4 is also a day-one test.

**The one rule that gets enforced mechanically:** `internal/domain` imports nothing that
performs I/O.

---

## 7. What BillyCore is trying to be

Not a personal script. The stated ambition is a **real, open-source product** — used by
people who are not the author, and genuinely new rather than a rearrangement of things
that already exist. The name is the intent: Billy is the bot that follows you through
your adult financial life.

Three consequences that apply from the first commit:

- **The repository is public, or will be.** No secrets in git, ever. `credentials.json`,
  `billy.db`, and every `.db-wal` are in `.gitignore` before the first commit, not after
  the first scare. A licence and a README are part of shipping.
- **Other people will read this code.** The documents are already written for an outside
  reader; the code has to match.
- **"Good enough for me" is not the bar.** But it is also not licence to gold-plate —
  see §2.

The honest failure mode, stated by the author: discovering the idea was not the
innovation it appeared to be. The fastest test of that is §3 step 5 — real emails,
real parsers, real transactions in a table — not more design.

---

## 8. Open — project level

1. **How is the result actually seen?** The success criterion requires a table, and
   PRODUCT.md says a UI is not Core's job. Preference stated: something *techy*. Likely
   candidates: `curl | jq`, a `billycore tx` CLI table, or a single static HTML page fed
   by `GET /v1/transactions`. Undecided, and it is on the critical path — the MVP is not
   demonstrable without it.
2. **What gets cut?** Nothing is currently on the cut list. Against ~78 hours, that is
   the largest risk in the project, larger than any technical unknown here.
3. **Repository layout — and the repository root is wrong.** `billycore`, `billysat`,
   and `billyagent` sit inside one git repository whose root is **`/Users/alexayala`,
   the home directory**, with no `.gitignore` at that root and zero commits. `~/.ssh/`,
   `~/.env`, and `~/.claude.json` are therefore untracked files inside a live repo, and
   `billycore/.gitignore` protects nothing above `billycore/`. Nothing has leaked —
   there are no commits — but the first `git add -A` from the wrong directory would.
   **Fix this before M1 step 3 writes a real refresh token to disk**, not before the
   first commit. Separately, for an open-source release these are probably three repos;
   that part is cheap now and annoying later.
4. **Licence.** Undecided. Required before the repository goes public.
5. **Contribution model.** Open source with outside contributors implies issues, a
   README, and a CONTRIBUTING file. None exist. Not needed before 2026-09-22.

---

> Update this file when the state changes — not when the design changes. Design lives in
> `docs/`; decisions live in `ai/DECISIONS.md`; this file answers *"where are we?"*
