# BillyCore — Decisions

**Last updated:** 2026-08-25

A running log of decisions that are settled. One entry per decision, newest at the
bottom, never rewritten in place — a decision that stops being true is **superseded** by
a new entry, not edited into agreement with the present.

This file exists because of the protocol in [CONTEXT.md](CONTEXT.md) §5: when work hits
an open question, stop, ask, and log the answer here. Documents in `docs/` describe the
system; this file records *when and why* each part of it was chosen, including the
options that lost.

**D1–D21 are backfilled** on 2026-08-23 from documents written 2026-08-22. They were
real decisions with real reasoning; they were simply recorded as prose in `docs/` before
this log existed. Where an entry says "Source", that document holds the full argument
and remains authoritative.

---

## How to use this file

| | |
|---|---|
| **Add an entry when** | An open question is closed, an option is rejected, or a design commitment is made that future work should not silently reverse |
| **Do not add an entry for** | Implementation details with one obvious answer, or anything `docs/` already states as settled design |
| **Status values** | `Accepted` · `Superseded by Dn` · `Reversed` |
| **Reversibility** | `cheap` · `bounded` · `expensive` — how much has to change if this turns out wrong |

Template at the bottom.

---

## Product shape

### D1 — BillyCore is self-hosted, open source, single-user
**Status:** Accepted · 2026-08-22 · Reversibility: expensive
**Decision.** The user runs BillyCore on their own machine. The financial data stays with
the person it belongs to.
**Why.** Mexican open finance does not exist in practice, and the companies selling
access charge heavily. A hosted Billy would recreate the thing it objects to.
**Consequence.** No multi-tenancy, no user table, no hosted auth. Every one of those
becomes a redesign, not a feature.
**Source.** PRODUCT.md

### D2 — Usable without AI
**Status:** Accepted · 2026-08-22 · Reversibility: expensive
**Decision.** BillyCore produces useful structured output on its own. AI is a consumer of
BillyCore, never a dependency of it.
**Why.** The product is infrastructure. Infrastructure that cannot run without a model
provider is not infrastructure.
**Consequence.** Ships D11 — deterministic parsers in v1, and everything classified in
month one is classified by hand-written Go.
**Source.** PRODUCT.md

### D3 — Delivered as a self-hosted service, not a library
**Status:** Accepted · 2026-08-22 · Reversibility: expensive
**Decision.** A daemon exposing endpoints. Not a library, not a batch export.
**Why.** Makes the database boundary physical: nothing reaches into BillyCore's data
except through its interface, regardless of who writes the consumer.
**Consequence.** Consumers are language-agnostic. BillyCore owns auth, versioning, and
process lifecycle. The user has one more thing to run.
**Source.** PRODUCT.md

---

## Architecture

### D4 — One binary
**Status:** Accepted · 2026-08-22 · Reversibility: bounded
**Decision.** A single process the user starts. No container, no orchestrator, no
separate database process, no migration step.
**Why.** An API process plus a worker process solves a scaling problem BillyCore does not
have. One person's financial email is not a throughput challenge.
**Consequence.** The split stays available later because the work queue lives in the
database (D7), making it a deployment change rather than a redesign.
**Source.** ARCHITECTURE.md §2

### D5 — Go
**Status:** Accepted · 2026-08-22 · Reversibility: expensive
**Decision.** Go, with a pure-Go SQLite driver (`modernc.org/sqlite`) and embedded
migrations.
**Why.** A statically linked binary *is* the artifact D3 describes. Chosen for the
delivery shape, not for preference.
**Consequence.** No cgo, so cross-compilation stays trivial and the storage path carries
no C memory-safety surface. Go's weak text-extraction ecosystem is made a non-issue by
D11.
**Source.** ARCHITECTURE.md §3

### D6 — Ports and adapters; the domain performs no I/O
**Status:** Accepted · 2026-08-22 · Reversibility: expensive
**Decision.** `internal/domain` imports nothing that performs I/O. Ports are interfaces
declared where they are consumed. No ORM, no separate persistence model.
**Why.** It is the mechanism that makes the DOMAIN.md boundary true in code rather than
in prose, and it is checkable in CI with one import-graph assertion.
**Consequence.** That assertion is a day-one test (D19). Invariants live in domain
constructors, so a Claim from HTTP and a Claim from a parser converge on one validator.
**Source.** ARCHITECTURE.md §4

### D7 — Staged, database-backed processing
**Status:** Accepted · 2026-08-22 · Reversibility: bounded
**Decision.** Ingestion records Evidence and stops. `RECEIVED → EXTRACTED → RECONCILED`,
each stage claiming rows from the database.
**Why.** Extraction is the part that fails. If it runs inside the fetch, a failure loses
the fetch — and the fetch may not be repeatable if the user deleted the email.
**Consequence.** Evidence immutability becomes real rather than aspirational. Pipeline
columns (`processing_stage`, `attempts`, `last_error`, `locked_until`) are infrastructure
and invisible to the domain.
**Source.** ARCHITECTURE.md §5

### D8 — HTTP + JSON at `/v1`, not gRPC
**Status:** Accepted · 2026-08-22 · Reversibility: bounded
**Decision.** Versioned HTTP with a JSON body.
**Why.** One consumer, no streaming requirement, and a developer who benefits more from
being able to `curl` the thing while debugging a parser at 1 a.m. Language-agnostic
consumption is satisfied by both options.
**Consequence.** The path is versioned so the choice stays reversible.
**Source.** ARCHITECTURE.md §6

### D9 — SQLite, not Postgres
**Status:** Accepted · 2026-08-22 · Reversibility: bounded
**Decision.** One file, `billy.db`, WAL mode, busy timeout. Relational, not document.
**Why.** The arguments for Postgres — JSONB for evidence payloads, full-text search for
merchant normalization — do not survive contact with the domain. Evidence is a blob, not
a document. Confidence is a table, not a JSON column. Merchant normalization has no
design yet.
**Consequence.** The user backs up their financial history by copying a file — which
D15 then has to reckon with. Repositories sit behind interfaces, so migrating later is
unpleasant but bounded.
**Source.** ARCHITECTURE.md §7

### D10 — Evidence identity is `(source_id, source_reference)`
**Status:** Accepted · 2026-08-22 · Reversibility: cheap
**Decision.** Unique per Source; ingestion is an upsert. For Gmail the reference is the
message id.
**Why.** Without it the pipeline duplicates Evidence on every re-fetch.
**Consequence.** A deduplication mechanism only. *Do two different artifacts describe one
Transaction?* stays a domain question owned by reconciliation.
**Source.** ARCHITECTURE.md §5, DATA_MODEL.md §4.1

### D11 — Deterministic parsers in Core; AI proposes through the public API
**Status:** Accepted · 2026-08-22 · Reversibility: bounded
**Decision.** Per-bank parsers in `internal/adapter/parser`. **No** `Extractor` port with
an LLM adapter inside Core. External extractors reach Core through `POST /v1/claims`.
**Why.** An internal LLM adapter creates two extraction pathways doing the same job —
two places the invariants can drift apart. Defensible under hexagonal architecture, and
still the wrong call.
**Consequence.** The cost is accepted honestly: month one is hand-written parsers. The
escape hatch is `POST /v1/claims`, which is why that endpoint ships in v1 rather than
being deferred.
**Source.** ARCHITECTURE.md §8

### D12 — Proposal endpoints ship in v1
**Status:** Accepted · 2026-08-22 · Reversibility: cheap
**Decision.** `POST /v1/claims` and `POST /v1/reconciliations` exist before BillyAgent
does. A proposal is not an instruction: Core validates and returns the accepted resource
or a `422` listing violations.
**Why.** Load-bearing consequence of D11, and the escape hatch when parsers fall short of
"well classified".
**Source.** ARCHITECTURE.md §6, API.md §8–§9

---

## Data model

### D13 — What is deliberately not modeled in v1
**Status:** Accepted · 2026-08-22 · Reversibility: cheap
**Decision.** No `accounts`, `currencies`, `merchants`, or `users` table. No Transaction
relationship table. `reconciliation_candidate` is not finalized.
**Why.** Each requires a domain decision that has not been made. A schema is not invented
to satisfy a document that ran ahead of the domain.
**Consequence.** A known mismatch is recorded rather than papered over: API.md exposes
`Transaction.relationships` before the domain has defined it. The domain question is
settled first, then the schema or the API changes.
**Source.** DATA_MODEL.md §9

---

## Security

### D14 — Source credentials live outside the database
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** A separate `credentials.json` beside `billy.db`, mode `0600`. BillyCore
refuses to read it if permissions are wider. OAuth scopes are read-only.
**Closes.** ARCHITECTURE.md open question 2.
**Why.** D9 makes "copy one file" the backup gesture. Refresh tokens inside that file
turn every backup, every `scp`, and every shared copy into a live credential leak that
keeps working after the copy is discarded.
**Rejected — OS keychain.** Better on security grounds; loses on delivery. Means cgo, a
different API per platform, an interactive unlock for a background daemon, and a store a
headless machine may not have. Contradicts D5. Revisit if Billy is ever packaged as a
desktop app.
**Source.** SECURITY.md §5

### D15 — No application-level encryption at rest in v1
**Status:** Accepted · 2026-08-23 · Reversibility: bounded
**Decision.** File permissions plus the user's full-disk encryption. No SQLCipher, no
per-column encryption.
**Why.** An encrypted database needs a key available to an unattended daemon at boot.
Storing that key next to the database is obfuscation, not encryption. A startup
passphrase is real security and breaks the daemon shape. SQLCipher also means cgo (D5).
**Consequence stated plainly.** A backup of `billy.db` is cleartext financial history and
belongs somewhere encrypted. This is user-facing documentation, not a footnote.
**Source.** SECURITY.md §6

### D16 — Evidence is hostile input
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** Parsers never execute or render Evidence, never resolve remote references,
disable XML external entities, bound size / time / recursion, and contain panics
per-artifact.
**Why.** The input feels trusted — it is the user's own email — and is not. Anyone who
knows the address can put bytes into the parser.
**Source.** SECURITY.md §7

### D17 — Sender authentication does not feed confidence in v1
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** DKIM/SPF/DMARC results are not used to weight Claim confidence yet.
**Why.** It is a genuinely good control and it is a *domain* decision — it changes what
confidence means. It does not get invented in a security document.
**Consequence.** Fabricated Evidence is mitigated only after the fact, by provenance and
retention. Carried as SECURITY.md open question 1, and probably the highest-value
security work available.
**Source.** SECURITY.md §8, §12

### D18 — One bearer token, loopback bind
**Status:** Accepted · 2026-08-22 · Reversibility: cheap
**Decision.** A single token read at startup, required on every `/v1` route, compared in
constant time. `/healthz` is open. No scopes, no rotation, no expiry, no TLS.
**Why.** A static token is weak authentication and is only adequate because the listener
is not reachable off-host. Loopback is the control; the token is the second line.
**Consequence.** BillyCore refuses to start with an empty token, and warns loudly if
bound to a non-loopback address. Per-consumer tokens become justified the day a second
consumer exists.
**Source.** ARCHITECTURE.md §6, SECURITY.md §4

---

## Working agreements

### D19 — Tests from day one
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** Tests are written alongside the first code, not after the first vertical
slice works. The D6 import-graph assertion is among the first.
**Why.** D6 makes the domain pure specifically so it can be tested without
infrastructure. That property is worthless unused.
**Source.** CONTEXT.md §6

### D20 — Two working modes
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** Mode 1, the human writes code and the agent reviews or fills placeholders.
Mode 2, the agent writes and the human reviews. The mode is stated at the start of a
session.
**Consequence.** Neither mode changes D19 or D6.
**Source.** CONTEXT.md §6

### D21 — Open questions are stopped at, not coded around
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** Work that reaches an open question stops, asks, and logs the answer here.
No silent picks, no coding around it.
**Why.** A decision made in passing inside an implementation is exactly what the six
design documents exist to prevent.
**Source.** CONTEXT.md §5

---

## Awaiting a decision

Not decisions — a pointer to what is knowingly unsettled, so nothing here gets closed by
accident. The authoritative lists are the open-question sections of each document.

| Question | Blocks | Where |
|---|---|---|
| How is the result actually seen? | The MVP demo | CONTEXT.md §8.1 |
| What gets cut from the ~78 hours? | Everything | CONTEXT.md §8.2 |
| Repository layout — one repo or three? | First commit | CONTEXT.md §8.3 |
| Licence | Going public | CONTEXT.md §8.4 |
| What does a reconciliation candidate reference? | Reconciliation, ~week 3 | DATA_MODEL.md Q1 |
| Where does the currency minor-unit exponent come from? | Rendering amounts | API.md Q8 |
| Reconciliation time window | Reconciliation | DOMAIN.md Q1 |
| Merchant normalization | Classification quality | DOMAIN.md Q2 |
| Can Evidence be deleted? | Deletion, retention | DATA_MODEL.md Q2, SECURITY.md Q2 |

### D22 — `billy.db` lives at `~/.billy/billy.db`
**Status:** Accepted · 2026-08-23 · Reversibility: cheap
**Decision.** A fixed directory in the user's home: `~/.billy/`, created `0700` at
startup. The database is `~/.billy/billy.db` and, per D14, credentials sit beside it at
`~/.billy/credentials.json`. Overridable by flag or environment variable; the default is
not the working directory and not an XDG path.
**Closes.** ARCHITECTURE.md open question 1.
**Why.** One predictable location the user can name, back up, and delete. A
working-directory default silently creates a second database the first time the binary
is started from somewhere else — the worst failure mode available, because it looks like
data loss. XDG is more correct on Linux and splits one conceptual thing across
`~/.local/share` and `~/.config` for a single-file product.
**Consequence.** One instance per user by default; a second requires an explicit
override. BillyCore creates the directory rather than requiring the user to. Directory
mode `0700` so the `0600` files in D14 are not merely reachable by a wider directory.
**Consequence — backup guidance changes.** D15 tells the user their backup gesture is
copying a file. That gesture must stay *"copy `~/.billy/billy.db`"* and must **not**
become *"copy `~/.billy/`"* — the directory now contains `credentials.json`, and D14
exists precisely so that backups do not carry live refresh tokens. This wording is
user-facing documentation, not an internal note.
**Source.** Author decision, 2026-08-23. Supersedes ARCHITECTURE.md §10 Q1.

### D23 — `source_reference` is the canonical name for Evidence identity
**Status:** Accepted · 2026-08-25 · Reversibility: cheap
**Decision.** The concept has one name in code and in the schema: **`source_reference`**.
ARCHITECTURE.md §5 calls it `source_artifact_key` and API.md uses that spelling on the
wire; both keep their wording, and the wire field is mapped to `source_reference` at the
HTTP boundary. For Gmail the value is the message id.
**Closes.** The naming collision found while scoping M1 (CONTEXT.md §5 item 1).
**Why.** Three names for one concept across three documents, and the domain constructor
had to name one regardless — whichever it named would become canonical silently.
`source_reference` wins because it is the name in DATA_MODEL.md, which is the document
describing the physical thing being named.
**Consequence.** DATA_MODEL.md §4.1 needs no change. A mapping exists in exactly one
place, the HTTP layer, and nowhere else. The second meaning DATA_MODEL.md §4.1 gives the
column — "the preserved reference when `raw_content` is NULL" — is untouched by this and
remains as written.
**Source.** Author decision, 2026-08-25.

### D24 — `content_type` becomes a column; the contract does not change
**Status:** Accepted · 2026-08-25 · Reversibility: cheap
**Decision.** Add `content_type TEXT` to the `evidence` table in migration 001. For Gmail
the value is `message/rfc822`, since Evidence stores the full RFC822 artifact.
`content_bytes` on the wire stays derived from `length(raw_content)` and is not stored.
**Closes.** The wire/schema gap found while scoping M1 (CONTEXT.md §5 item 2).
**Why.** The simplest of the two available choices. API.md §4 and §6 already carry
`content_type`, so adding one column makes the code match a contract that is already
written, while the alternative means amending the public interface document. Migration
001 has not run anywhere, so the column costs nothing now and a second migration later.
**Rejected — drop `content_type` from API.md.** Defensible: nothing in M1 reads it, and
the smallest schema is a virtue. It loses because it changes the contract to match an
implementation gap rather than the reverse, and because the value is genuinely useful the
moment a second Source exists — a PDF statement and an email are not the same kind of
artifact, and `GET /v1/evidence/{id}` is the endpoint that has to say which it handed you.
**Consequence.** DATA_MODEL.md §4.1's table definition is now one column behind the
schema BillyCore actually creates. That is a documentation correction for the author to
make, not a licence for the code to drift further.
**Source.** Author decision, 2026-08-25.

### D25 — `EvidenceIngested` is emitted in M1; `domain_event` is in migration 001
**Status:** Accepted · 2026-08-25 · Reversibility: cheap
**Decision.** M1 emits `EvidenceIngested`. Migration 001 creates `domain_event` alongside
`evidence`. The event is written in the same SQLite transaction as the row that produced
it, and **only when the insert actually created a row** — a duplicate that hits
`ON CONFLICT (source_id, source_reference) DO NOTHING` emits nothing. Payload is what
DOMAIN.md §8 specifies: `evidenceId`, `sourceReference`, and the observed timestamp.
`GET /v1/events` is not in M1; the events are written and not yet read.
**Closes.** CONTEXT.md §5 item 5.
**Why.** DATA_MODEL.md §4.7 requires an event to be persisted in the same transaction as
its change, and calls a change that commits without its event invisible to every
consumer. The first ~1,044 Evidence rows are the ones every later Claim and Transaction
traces back to; leaving them without an event makes the audit anchor start late. The
alternative costs more the longer it waits, which is the definition of a decision that
should not be deferred.
**Rejected — Evidence only, `domain_event` in migration 002.** Genuinely smaller, and it
keeps M1's scope exactly at "Gmail → Evidence → SQLite". It loses because the two ways to
recover afterwards are both bad: a backfill stamps `occurred_at` with a time the
ingestion did not happen — a lie in an append-only log whose whole value is ordering —
or the first rows keep a permanent hole and DATA_MODEL.md §4.7's rule becomes a rule that
holds from migration 002 onward, which is not a rule.
**Consequence.** The repository's insert must report whether a row was created, which
M1's idempotency requirement already demanded independently. Writing evidence and event
in one transaction fixes the repository's shape now rather than after there is data.
DOMAIN.md Q10 — which events v1 needs — stays open for the other five; this entry answers
it for `EvidenceIngested` only.
**Source.** Author decision, 2026-08-25. DATA_MODEL.md §4.7, DOMAIN.md §8.

### D26 — Source configuration lives in `~/.billy/sources.json`
**Status:** Accepted · 2026-08-25 · Reversibility: cheap
**Decision.** A `sources.json` beside `billy.db` and `credentials.json`, read at startup,
mapping a Source id to its type and its fetch configuration. For Gmail that is a search
query:

```json
{ "sources": [ { "id": "gmail_primary", "type": "GMAIL", "query": "from:nu@nu.com.mx" } ] }
```

It holds **no credentials** — those stay in `credentials.json` at `0600` (D14), and the
split is the point: a Source is configuration, a refresh token is a secret, and they have
different lifetimes and different blast radii. A missing file means no Source is
configured, which is not a startup failure: `POST /v1/sources/{id}/sync` answers `404`
for an id it does not know, exactly as API.md §5 specifies.
**Closes.** CONTEXT.md §5 item 3.
**Why.** `POST /v1/sources/{id}/sync` promises a `404` for an unconfigured Source, so
something has to hold the set of configured Sources. A file is the shape this ends up in
regardless — adding a second mailbox should not require rebuilding the binary — and
writing it now costs one small parser rather than a migration and a write path later.
**Rejected — a compile-time registry in the composition root.** Cheaper today, about
fifteen lines, and it invents no format. It loses because the format gets invented
anyway the first time the user wants a Source the author did not compile in, and by then
there is a working system to change rather than an empty one.
**Rejected — a `source` table and migration 002.** Backs up with the database and is
queryable, but D13 says there is no Source table in v1, and rows have to get in somehow:
either another endpoint or hand-written SQL. The most work of the three, for a set that
has one element.
**Consequence.** BillyCore reads a second file at startup, and a malformed one is a
startup error rather than a silent empty set — a typo'd `sources.json` that reported "no
Sources configured" would surface as a `404` from sync, which is the least informative
possible way to learn about it. `credentials.json` stays the only `0600` file; the
backup guidance in D22 is unchanged, because `sources.json` carries nothing secret.
DATA_MODEL.md and D13 are untouched: this is configuration, not a domain entity, and no
table is created.
**Source.** Author decision, 2026-08-25.

### D27 — Bank statements and reconciliation are in the MVP
**Status:** Accepted · 2026-08-25 · Reversibility: expensive
**Decision.** The MVP is not "Nu email in a table". It is **Nu email plus the Nu card
statement, reconciled**. Statements become a second Source of `source_type`
`BANK_STATEMENT`, and DOMAIN.md §6 reconciliation ships rather than being deferred to
v2.
**Why.** Two reasons, and the second is the real one. First, with two Sources the same
transfer genuinely appears twice — an email and a statement line — so deduplication
stops being hypothetical. Second and larger: **Nu does not email card purchases**
(CONTEXT.md §3.1 — zero in 1,044 messages), so the statement is the only place they
exist. An email-only table shows money moving between accounts; it cannot answer *what
did I buy*, which is most of what PRODUCT.md's success sentence means by "well
classified".
**Rejected — cut reconciliation, ship the email table.** Argued for on 2026-08-25 on the
grounds that one Source with one email per event has nothing to reconcile, and that
cutting it removes ~20 hours and five open questions from the critical path. It loses
because its premise was wrong: the author intends statements, and the single-Source
world it assumed was never the plan. Recorded because the reasoning was sound given the
premise, and the premise is the part that failed.
**Consequence — statements arrive by hand.** Measured 2026-08-25: all 23
`estado de cuenta` emails are `text/html` with **no attachment**, carrying links behind
an app login, and D16 forbids BillyCore resolving them. The 12 PDFs in the mailbox are
contracts, not statements. So intake is `POST /v1/evidence` (API.md §6) with the user
supplying the file — which is why that endpoint ships rather than being deferred.
**Consequence — open questions become blocking.** DOMAIN.md Q1 (time window), Q3
(different amounts), Q7 and Q8 (which contradictions force `NO_MATCH` vs `AMBIGUOUS`),
Q15 (does one Transaction absorb the other), and DATA_MODEL.md Q1 (what a reconciliation
candidate references) all move from "week three at the earliest" onto the path. Each
still gets stopped at and logged (D21).
**Consequence — the budget does not fit.** ~80–90 hours of work against ~72 remaining.
Resolved by D28.
**Source.** Author decision, 2026-08-25.

### D28 — The deadline moves; the scope does not
**Status:** Accepted · 2026-08-25 · Reversibility: cheap · Supersedes the "hard" deadline in CONTEXT.md §2
**Decision.** **2026-09-22 stops being a hard deadline.** It remains the target. When the
two collide, the date gives way and the scope in D27 is delivered whole.
**Closes.** CONTEXT.md §8.2 — *what gets cut?* Nothing.
**Why.** The deadline was self-imposed and binds one person. The scope is what makes the
product the thing PRODUCT.md describes rather than a demonstration of plumbing. Shipping
a table without card spending on time would meet a date and miss the point.
**Rejected — reconcile by hand for the MVP.** Build both parsers, show duplicate
transfers, defer automatic reconciliation to M3. Saves ~20 hours and four open questions
and keeps the date. It loses because the author chose the scope over the date, plainly,
when both were put side by side.
**Rejected — statement only, drop the email parsers.** The statement carries card
purchases *and* transfers, so one Source could produce the whole table and reconciliation
would have nothing to do. Genuinely cheaper. Not chosen; the email path is already built
as far as Evidence and the author wants both.
**Consequence.** CONTEXT.md §2's "**2026-09-22 — hard**" is now false and is corrected.
The estimate on the table is **early-to-mid October** at ~18 h/week. Nothing else about
§2 changes: the success criterion is untouched, and "nothing that does not serve that
sentence gets built" applies with more force now, not less, because the scope grew.
**Consequence — a deadline that moves once can move again.** The honest risk this
creates is that it stops being a constraint at all. The mitigation is that D27 fixed the
scope in writing: the date moves for *that* list, and a new item on it is a new decision.
**Source.** Author decision, 2026-08-25.

### D29 — A Nu wall-clock value is America/Mexico_City
**Status:** Accepted · 2026-08-25 · Reversibility: bounded
**Decision.** Every date and time parsed out of a Nu artifact is interpreted in
**`America/Mexico_City`**, and stored as UTC RFC 3339 per DATA_MODEL.md §2. The parser
layer keeps returning a zoneless `parser.Wall`; the conversion happens once, at an
explicit call site in the Nu template parser, rather than being implied by a type.
**Closes.** The question raised on 2026-08-25 while building `internal/adapter/parser`:
no Nu email names a zone, and DATA_MODEL.md §2 requires one.
**Why.** Nu México is a Mexican institution and the account is a Mexican one, so the
wall clock in the email is Mexico City's. The alternative is not a smaller assumption —
it is a different one, six hours wide.
**Rejected — stamp UTC and move on.** Free, and the reason it loses is precisely that it
is free: nothing in the Evidence would ever contradict it. A 21:16 transfer on 20 July
becomes the 21st, and a month-end one moves into the next month — which is the boundary
PRODUCT.md's "last month of transactions" is drawn on. An error no input can reveal is
the definition of quietly wrong.
**Rejected — the machine's local zone (`time.Local`).** Looks like deference and is
actually non-determinism: the same artifact re-parsed on a laptop in Mexico and a VPS in
Frankfurt produces two different Transactions, and a re-parse stops being reproducible.
BillyCore is self-hosted (D1); where it is hosted must not change what it concludes.
**Consequence — `import _ "time/tzdata"` in the binary.** D4 is one binary, and
`time.LoadLocation` otherwise depends on a system tz database that a scratch container
does not carry. The failure would be at runtime, on a machine the author is not sitting
at. Measured 2026-08-25: ~413 KB on a ~16 MB binary. No `go.mod` change — `time/tzdata`
is standard library, so SECURITY.md §11 does not apply.
**Consequence — the stored corpus contains no ambiguous local time.** Mexico abolished
DST on 2022-10-30, before the earliest artifact (2023-08-23), and `America/Mexico_City`
is a constant −06:00 across 2023–2026 — verified against Go's tz database, not assumed.
This is a fact about today's data, not a property of the decision. If DST returns, or a
bank statement ever reaches back past 2022, the template parser meets its first
ambiguous or nonexistent local time and that is a new question, not this one.
**Consequence — this is a Source property, not a global one.** The decision is "Nu
artifacts are Mexico City", not "Billy is Mexico City". A Source in another country
brings its own zone.
**Source.** Author decision, 2026-08-25.

### D30 — A Nu amount is MXN
**Status:** Accepted · 2026-08-25 · Reversibility: cheap
**Decision.** Amounts parsed from Nu artifacts carry `Currency("MXN")`. `ParseMoney`
continues to take the currency from its caller; the Nu template parser is the caller
that supplies it.
**Closes.** The question raised on 2026-08-25 while building `internal/adapter/parser`:
`ParseMoney` must produce a Money, and Money without a currency is not one (DOMAIN.md
§3).
**Why.** Measured across all 1,044 artifacts: `MXN`, `USD`, `pesos` and `M.N.` appear
**zero** times. Currency is therefore not a field these emails contain — it is an
inference about the Source, and Nu México issues MXN accounts.
**Consequence — the inference is recorded where it is made.** Not inside `ParseMoney`,
which would quietly make every future Source Mexican; at the Nu template parser, which
is the only place that knows which Source it is reading.
**Consequence — `$` is not evidence of MXN.** Many currencies use the glyph. The
reasoning is the Source, and a Source that could carry more than one currency — a card
statement with a foreign-currency line — must state it rather than have it inferred.
**Consequence — API.md Q8 stays open.** This decision names the currency, not its
minor-unit exponent. `ParseMoney` still requires two fraction digits and refuses `$300`,
so nothing here depends on an exponent table.
**Open — does an inferred field carry the same confidence as a parsed one?** MXN is a
field Billy will assert with no support in the artifact, which is a different thing from
`Monto: $1,000.00`. DOMAIN.md §7 does not distinguish them. Not decided here; it lands
when Claims grow field-level confidence.
**Source.** Author decision, 2026-08-25.

### D31 — `¡Recibimos tu pago!` is an OUTFLOW
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** The card payment receipt records money **leaving** the user: it is a
payment against the balance owed on their Nu credit card. `parseCardPayment` sets
`Direction: OUTFLOW` for all 90 artifacts.
**Closes.** The question left open by the card payment parser on 2026-08-25, which
extracted those 90 artifacts with no direction rather than guessing (D21).
**Why.** The author's account of what the message means: a debit paying down card debt.
Money left an account they hold and reduced what they owe.
**Consequence — the direction is stated from the paying account's point of view.** The
same event seen from the card's side is a credit. Nothing in this artifact names the
account the money came *from*, so the Transaction this produces describes the payment,
not the card's balance.
**Consequence — this is the first real double-count risk, and it lands in
reconciliation.** When the card statement arrives as a second Source (D27), the same
payment appears there as a credit on the card. Two artifacts, one movement of money,
opposite signs. DOMAIN.md §6 has to collapse them rather than record an OUTFLOW and an
INFLOW that cancel — and the amount/time signals will match while the direction signal
contradicts, which is exactly the shape §6 treats as grounds to *block* a match. This is
a known problem to be solved there, not here.
**Consequence — a card payment is still dateless.** `OccurredAt` stays zero: the body
carries no timestamp, and DATA_MODEL.md §4.5's fallback to the Source's delivery
timestamp remains the caller's to apply. D31 answers direction and nothing else.
**Source.** Author decision, 2026-08-26.

### D32 — The result is seen two ways: a terminal table and a plain web page
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** BillyCore ships **both** surfaces, and both read the same
`GET /v1/transactions`:
1. **`billycore tx`** — a table in the terminal, from the same binary as `serve` (D4).
2. **A single plain HTML page** — one file, no framework, no build step, no client
   state, rendering the same rows as a table.
**Closes.** CONTEXT.md §8.1 — *how is the result actually seen?* — which was on the
critical path, because the success criterion is a table and none of the candidates had
been chosen.
**Why.** The criterion in PRODUCT.md is *"I can see my last month of transactions, well
classified, in a good table with good financial information."* `curl | jq` does not
satisfy the word *table*, and the author wants both a terminal view and a browser one.
**Consequence — the API is the contract, not the renderer.** Both surfaces are thin: a
change to what a Transaction means changes `GET /v1/transactions`, and the two views
follow. Neither may compute a financial fact of its own.
**Consequence — this stays a page, not a UI.** PRODUCT.md is explicit that a UI is not
Core's job; that is BillySat. The line held here is that the page has no framework, no
build step, no client-side state and no route but its own. The moment it wants a second
screen, it has stopped being this and is BillySat's problem.
**Open — does Core serve the page, or is it a file the user opens?** Serving it from the
binary is the only version that works without a second server, and D18's single bearer
token then has to reach a browser, which is friction a `curl` call does not have.
Not decided here. It is the part of this decision most worth arguing with.
**Source.** Author decision, 2026-08-26.

### D33 — `occurred_at` is a Claim field; the vocabulary is seven names
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** `occurred_at` joins the closed `FieldName` set in `internal/domain`,
alongside the six DATA_MODEL.md §4.4 writes down. It is a text field holding a UTC
RFC 3339 timestamp, and it carries its own confidence like every other field.
**Closes.** The gap surfaced on 2026-08-26 while building the Claim aggregate:
DATA_MODEL.md §4.5 has `occurred_at` as an always-populated *Transaction* column, but
§4.4's Claim vocabulary does not list it, so nothing carried the event time from a
parsed email to a Transaction.
**Why.** The event time is a fact extracted from Evidence, exactly like the amount or
the merchant, and it is extracted with the same fallibility. Passing it to the
Transaction through a separate path would give one interpreted value a private channel
that skips Claim validation, skips provenance, and skips confidence — three properties
every other extracted value has to earn.
**Consequence — the card payment stops being a special case.** All 90
`¡Recibimos tu pago!` artifacts carry no body date, so they simply have no `occurred_at`
field: no row, no value, no belief. DATA_MODEL.md §4.5's fallback to the earliest
`observed_at` then fires when the Transaction is built. That is DOMAIN.md §7's
absence-versus-low-confidence distinction doing the work it was designed for, rather
than the extractor inventing a timestamp on the side.
**Consequence — the fallback stays outside the domain.** §4.5 already requires it to be
computed before writing the Transaction and never inside a SQL query. The Claim records
what the artifact said; the step that builds a Transaction applies the rule.
**Consequence — DATA_MODEL.md §4.4's field table is now one row short.** The author's
correction to make, like the §4.1 drift D24 left behind.
**Rejected — store the timestamp as `value_int` unix seconds.** Cheaper to compare and
sort. It loses because DATA_MODEL.md §2 already settled that timestamps are UTC RFC 3339
text everywhere in this schema, and one column that disagrees is worth more confusion
than it saves.
**Still open — the identifiers.** Nu's `folio`, `clave de rastreo` and `concepto` remain
outside the vocabulary. The tracking key is the one that matters, because DOMAIN.md §6
wants it as a reconciliation signal; it appears on 16 of 1,044 artifacts and never on an
inflow. Not decided here.
**Source.** Author decision, 2026-08-26.


### D34 — Claim confidence tracks how the artifact yielded the value
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** One rule, applied at the Nu interpreter:

| | |
|---|---|
| `HIGH` | the artifact states it on a labelled line |
| `MEDIUM` | the artifact implies it — read from prose, from position, or from the template's own identity |
| `LOW` | no artifact states it at all; Billy inferred it from the Source |

`currency` is therefore **LOW on every Claim**, which closes the question D30 left
open — *does an inferred field carry the same confidence as a parsed one?* It does not.
**Closes.** D30's open item, and the confidence half of DOMAIN.md §7 for the four Nu
templates.
**Why.** Confidence is about *support*, and support is a property of the reading rather
than of the value. `Monto: $1,000.00` under a label is an anchor Nu would have to change
its template to break; "…a la cuenta de <name> en <bank>…" is a sentence Nu can reword in
a marketing pass, and a reword yields a *wrong name* rather than no name. MXN is neither:
it appears zero times in 1,044 artifacts.
**Consequence — the rule lives beside the parsers, not in `internal/app`.** Only the code
that did the reading knows which of the three happened. The use case must not re-derive it
by guessing the layout from which other fields happen to be present.
**Consequence — `parser.Extraction` gained `CounterpartyLabelled`.** The value alone
cannot say whether a label or a sentence produced it, and inferring the layout from the
presence of `Estatus:` would be exactly the clever guess this decision forbids.
**Consequence — every Claim carries a LOW field, so LOW stops being a review flag.** The
count of LOW fields is now the count of Claims. This was named as the cost when the
decision was taken and accepted.
**Consequence — `account_identifier` is never claimed, on any template.** Nu's
account-shaped values — `Tarjeta de débito: ••••7662`, the service payment's `Número de
cuenta:` — all belong to the *other* party. DOMAIN.md §6 treats a known-account
contradiction as grounds to block reconciliation, so claiming one as the user's would not
merely be wrong; it would prevent correct matches later (CONTEXT.md §3.1).
**Consequence — the card payment claims no `merchant`.** Its counterparty is the user's
own card product, which is not a counterparty. 90 artifacts get no belief rather than a
plausible-looking wrong one.
**Source.** Author decision, 2026-08-26.

### D35 — A Nu outflow receipt is SETTLED; the other three templates claim no status
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** All **354** `Tu transferencia fue exitosa` artifacts carry
`financial_status = SETTLED` — `HIGH` on the 16 that also say `Estatus: Completada`,
`MEDIUM` on the 338 where the belief rests on the subject line. The inflow, card payment
and service payment templates carry **no `financial_status` row at all**.
**Closes.** The question raised on 2026-08-26 while building the extraction slice.
**Why.** The subject line is itself the assertion — *your transfer was successful* — and a
SPEI transfer that succeeded is final. Restricting SETTLED to the 16 artifacts carrying
the explicit label would discard 338 statements that say the same thing in the subject
rather than in a field.
**Consequence — absence, not `UNKNOWN`.** The 446 artifacts that state nothing get no row.
Writing `financial_status = UNKNOWN` would assert a belief identical to absence and store
it twice, erasing the distinction DOMAIN.md §7 insists stays sharp. The Transaction
defaults to `UNKNOWN` in slice 4 without Billy claiming it.
**Consequence — the confidence split is the honest part.** 338 of the 354 rest on a
subject line, and `MEDIUM` is what says so.
**Open — should an inflow receipt be SETTLED too?** `¡Recibiste una transferencia!` means
the money is in the account, which is arguably as settled as an outflow. It was not
decided: the decision taken names 354, and 345 inflows were left claiming nothing.
**Source.** Author decision, 2026-08-26.

### D36 — `tracking_key` joins the vocabulary; it is eight names
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** `tracking_key` — the SPEI `Clave de rastreo` — joins the closed `FieldName`
set in `internal/domain`, alongside the seven D33 left it at. Text, opaque, kept verbatim,
`HIGH` where present.
**Closes.** The identifier question D33 left open by name.
**Why.** SPEI tracking keys are globally unique, so two artifacts sharing one are the same
movement with no ambiguity — the strongest signal DOMAIN.md §6 can be given.
**Consequence — it earns its place against the second Source, not this mailbox.** It
appears on **16** of 1,044 artifacts and **never on an inflow**, so the two halves of a
transfer between the user's own accounts can never be matched by it. Within Gmail alone it
buys nothing; against the bank statement D27 adds, it may buy everything.
**Consequence — no migration.** `claim_fields.field_name` is TEXT with no CHECK, because
DATA_MODEL.md §2 puts that vocabulary in the domain. Migration 002 needed no change, which
is that decision paying out.
**Rejected — folio, concepto, número de referencia, código de operación.** Each remains a
vocabulary decision of its own. Nothing is lost by waiting: Evidence is immutable and
retained, so a later decision re-parses all 1,044 artifacts for free.
**Source.** Author decision, 2026-08-26.

### D37 — A parser-produced Claim is born PROPOSED and activated in the same transaction
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** The extraction use case calls `domain.NewClaim(..., ClaimProposed, ...)` and
then `Activate`, and the store writes the Claim, its fields, its provenance, the
`ClaimActivated` event and the Evidence stage advance in **one** transaction.
**Closes.** The question raised on 2026-08-26: DOMAIN.md §5 has both states and §8 has the
event, and nothing said which one a parser produces.
**Why.** `Activate` is the only operation that performs the transition `ClaimActivated`
describes. Constructing at `ACTIVE` would put an event in the log for a transition no code
ever made — the log would describe an activation that did not happen.
**Consequence — `created_at` equals `updated_at`.** The two states occupy one instant. That
is honest: the Claim really was proposed and really was accepted, and nothing happened in
between.
**Consequence — it is the path `POST /v1/claims` will take** when an outside proposer
offers an interpretation (D11, D12), with the difference that theirs may stop at
`PROPOSED`.
**Consequence — the stage advance is inside the Claim's transaction.** D25's argument
applied twice: an event describing a change that did not commit is a lie about the domain,
and Evidence marked `EXTRACTED` whose Claim rolled back is a worse one — an artifact Billy
never revisits and has nothing to show for. Artifacts that produce *no* Claim advance
through `EvidenceQueue.MarkExtracted` instead.
**Source.** Author decision, 2026-08-26.


### D38 — At most one ACTIVE Claim per piece of Evidence
**Status:** Accepted · 2026-08-26 · Reversibility: moderate — it is a schema constraint
**Decision.** `evidence_active_claim (evidence_id PRIMARY KEY, claim_id)`, migration 003.
A row exists only while a Claim is `ACTIVE`. The primary key is the invariant.
**Closes.** The question raised on 2026-08-26 while making extraction idempotent: nothing
in `docs/` said how many Claims one artifact may have live at once.
**Why.** DOMAIN.md §5 says `ACTIVE` means "the Claim Billy currently uses". Two of those
for one artifact is not a richer answer, it is the absence of one — there is no rule for
which a Transaction should be built from. Supersession exists precisely to move from one
live interpretation to the next.
**Consequence — extraction is idempotent by constraint, not by checking.** M1's ingestion
is idempotent because `UNIQUE (source_id, source_reference)` says so, not because `Insert`
remembered to look. Extraction now earns the property the same way. `Save` reports
`created=false` on a collision, exactly as `Insert` does for an already-recorded artifact.
**Consequence — the duplicate this prevents needs no crash.** A pass claims a row on a
one-minute lease, runs long, and a second pass claims the same row. Both write a Claim;
the stage guard does not help, because the second `UPDATE` matches no row and the Claim
lands anyway. Measured: eight goroutines racing one artifact produce exactly one Claim and
one `ClaimActivated`, over twenty runs under `-race`.
**Consequence — the proposal endpoints still work.** A `PROPOSED` Claim takes no slot, so
an outside proposer (D11, D12) may offer a competing interpretation of an artifact Billy
has already interpreted. It collides only on activation, and there colliding is correct:
activating a second interpretation *means* superseding the first, which is what
`ClaimActivated`'s `supersededClaimId` records.
**Rejected — `UNIQUE` on `claim_evidence(evidence_id)`.** The obvious constraint, and it
forbids that competing proposal outright, which is the entire point of D11.
**Rejected — deterministic Claim ids over the existing primary key.** Needs no new schema
at all. It loses on the case that matters: after a parser is fixed, re-extraction would
produce the same id and be silently skipped, blocking the improvement rather than
superseding the old Claim with a better one.
**Consequence — the transaction tables move to 004.** They were expected to be 003.
**Source.** Author decision, 2026-08-26.

### D39 — Extraction retries back off exponentially, and never stop
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** A failed extraction attempt backs the row off by `RetryBase * 2^(attempts-1)`,
capped at `RetryCap`. `RetryBase` is one minute, `RetryCap` is 24 hours, and there is
**no maximum attempt count**.
**Closes.** The gap DATA_MODEL.md §7 leaves: it specifies the mechanism — `attempts`,
`last_error`, `locked_until` — and names no schedule, the way ARCHITECTURE.md §7 specified
a busy timeout without naming a value.
**Why no maximum.** The failure that actually happens here is Nu changing a template: a few
hundred artifacts start failing and the fix is a new parser. A capped backoff means those
rows retry themselves within a day of the fix shipping, with nobody resetting anything. A
row parked after N attempts needs a hand to bring it back — and DATA_MODEL.md §7 has
already ruled out expressing "failed" as a stage, so there is no clean place to see the
parked set either.
**Consequence — a genuinely poisonous artifact costs one parse a day, forever.** Accepted:
that is cheap, and the alternative silently loses data on the day a parser is fixed.
**Consequence — `attempts` counts hand-outs, not handled failures.** It increments in the
same statement that takes the lock. A parser that takes the whole process down with it —
an OOM, a SIGKILL — never reaches code that could record a failure, and a count of handled
failures would leave that row at zero forever, retried on every pass for the life of the
database.
**Consequence — retry is still not a stage.** Evidence that failed extraction stays at
`RECEIVED`, exactly as received.
**Source.** Author decision, 2026-08-26.

### D40 — The inflow receipt is SETTLED too
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Supersedes.** D35's scope, and only its scope. D35's reasoning for the outflow receipt
stands unchanged; this widens the set it applies to.
**Decision.** `¡Recibiste una transferencia!` carries `financial_status = SETTLED` at
`MEDIUM`, joining the 354 outflow receipts. **699 of 800** Claims now assert a status.
**Why.** The subject line is the assertion, and it asserts the same fact from the other
end: the money is in the account. A SPEI transfer is final either way, and the receiving
side is no less settled for having been written from the recipient's point of view.
Restricting SETTLED to the sending side recorded a difference in *who wrote the email*
rather than a difference in what happened to the money.
**Consequence — no special case in the code.** No inflow carries an `Estatus:` line, so
all 345 land at `MEDIUM` through D34's ordinary rule — the belief rests on the subject
alone — rather than through an exception written for them. The condition that changed is
one template name.
**Consequence — the card payment and the service payment still claim nothing.** Neither
states anything about settlement, and DOMAIN.md §5's `UNKNOWN` is what the Transaction
defaults to without Billy claiming it. Absence stays distinguishable from belief.
**Source.** Author decision, 2026-08-26, answering the item D35 left open by name.

### D41 — The natural key of a Transaction is the Claim it was built from
**Status:** Accepted · 2026-08-26 · Reversibility: moderate — it is a schema constraint
**Decision.** `claim_transaction (claim_id PRIMARY KEY, transaction_id)`, migration 004.
`TransactionRepository.Save` takes the originating Claim id, claims the slot with
`ON CONFLICT DO NOTHING`, and reports `created=false` when it loses. Re-running
reconciliation writes nothing.
**Closes.** The question raised on 2026-08-26 at the start of slice 4: slice 3's answer was
one ACTIVE Claim per Evidence, and the equivalent here is not obvious. Getting it wrong
duplicates money in a table.
**Why the Claim rather than the Evidence.** The closed eight-name field vocabulary already
means one Claim describes exactly one movement, whatever the artifact it came from held.
Keying on the artifact says instead that one artifact yields at most one Transaction —
true of all 1,044 emails, and false of the bank statement D27 put in the MVP, which is one
PDF and forty movements. A constraint that has to be dropped one slice later is not the
natural key.
**Why a constraint and not a check.** The same argument D38 made, and the same race: a pass
runs longer than its one-minute lease, a second pass claims the row, and both build. The
stage guard does not help — the second `UPDATE` matches no row and the Transaction lands
anyway. Measured: eight goroutines racing one Claim produce exactly one Transaction and one
`TransactionCreated`.
**Rejected — `UNIQUE (evidence_id)` on `transaction_evidence`.** One line, no new table,
and correct for every artifact in the mailbox. It encodes "one artifact, at most one
Transaction", which the statement Source breaks.
**Rejected — a `source_claim_id` column on `transactions`.** Same idempotency with one
fewer table. It adds a column DATA_MODEL.md §4.5 does not list and a claims→transactions
foreign key §6's "foreign keys that exist" does not name, so it is a docs change as well as
a schema one — and it puts an infrastructure fact on the aggregate root.
**Rejected — a content key of amount, currency, direction and occurred_at.** Two 500 MXN
transfers to the same person in one minute would become one, and once the `occurred_at`
fallback lands the 90 card payments derive their time from `observed_at`, so near-identical
rows would collide by construction. Deciding that two observations are one event is
DOMAIN.md §6's job, with six signals, not a `UNIQUE` index's.
**Consequence — a superseding Claim takes a free slot and builds a second Transaction.** A
fixed parser produces a better Claim, and the Transaction built from the old one is still
in the table. Retiring it is a decision this slice does not take and does not need; it is
the price of keying on the interpretation rather than the artifact, and it is the direction
that leaves the improvement reachable rather than silently skipped.
**Consequence — `transaction_evidence` carries no uniqueness.** It stays what DATA_MODEL.md
§4.6 describes: a join table taking no position on how many artifacts support one
Transaction.
**Source.** Author decision, 2026-08-26.

### D42 — One ACTIVE Claim produces one Transaction
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** The reconciliation pass builds exactly one Transaction per ACTIVE Claim, born
`UNRECONCILED`. It merges nothing. Against the corpus that is 800 Claims and 800
Transactions — 710 today, and the remaining 90 once the `occurred_at` fallback lands.
**Closes.** The question raised on 2026-08-26: whether reconciliation later merges
Transactions or Claims.
**Why.** DOMAIN.md §8's `TransactionReconciled` already speaks of a "surviving
transactionId", and the API design has used Transaction ids throughout. Merging at build
time would also put reconciliation logic inside the builder, where DOMAIN.md §6's other
five signals cannot reach it.
**Rejected — merge on `tracking_key` at build time.** It reaches 16 of 1,044 artifacts and
never an inflow, so the two halves of a transfer — the pair it would exist to merge — can
never be matched by it against this mailbox. D36 already said the key earns its place
against the second Source, not against this one.
**Consequence — the two halves of a self-transfer are two Transactions** until
reconciliation runs. That is the honest state: Billy has two observations and has not yet
decided they are one event.
**Consequence — it makes DATA_MODEL.md Q1 answerable**, and does not answer it. A
reconciliation candidate referencing Transaction ids is now the reading the code supports;
the entry that settles it is still to be written, with the schema it implies.
**Source.** Author decision, 2026-08-26.

### D43 — A Transaction whose Claim asserts no status is UNKNOWN
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** Where the Claim carries no `financial_status` field, the Transaction takes
`UNKNOWN`. 101 of 800 Claims: the 90 card payments and the 11 service payments. The domain
constructor still *rejects* the empty status — the substitution is the use case's, made
explicitly, and is not a default hidden inside `NewTransaction`.
**Closes.** The question raised on 2026-08-26, and the item D40 left standing when it wrote
that "`UNKNOWN` is what the Transaction defaults to without Billy claiming it" without
anything yet implementing it.
**Why.** DOMAIN.md §5 makes `UNKNOWN` a legitimate state precisely for this: Evidence may
not reveal whether an event is an authorization or a settlement. Recording it is Billy
saying it looked and cannot tell, which is a fact rather than a gap.
**Rejected — SETTLED, because all four templates are receipts.** Defensible: a card
payment email means the payment went through. It records an inference the artifact never
made, and it makes "Billy could not tell" indistinguishable from "Billy read it" in the one
column a balance depends on.
**Rejected — a nullable `financial_status`.** Absence would stay absence, as it does for a
Claim field. It contradicts DATA_MODEL.md §4.5, where the column is `NOT NULL`, and gives
`UNKNOWN` a second spelling.
**Consequence — the constructor rejects `""` and accepts `UNKNOWN`.** A caller that forgot
the field must not look like one that read the artifact and could not tell.
**Source.** Author decision, 2026-08-26.

### D44 — RECONCILED means the pipeline is finished with a row, not that it carries a Transaction
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** The 244 artifacts that produced no Claim advance from `EXTRACTED` to
`RECONCILED` carrying nothing. `ReconcileQueue.ClaimForReconciliation` returns them rather
than filtering them out, and the pass advances them through `MarkReconciled`.
**Closes.** The question raised on 2026-08-26: whether `RECONCILED` is for every row or
only for rows that carry a Transaction.
**Why.** ARCHITECTURE.md §5 is explicit that stage is pipeline position and not domain
state, and the precedent is already in the code: `MarkExtracted` advances artifacts nothing
recognised, so that a pass does not re-read all 244 of them forever. The same argument
applies one stage later, unchanged.
**Rejected — leaving them at `EXTRACTED`.** It makes the stage column readable as a claim
about the data, which is the more attractive reading. It also creates a permanent 244-row
backlog that every reconciliation pass claims, finds nothing in, and releases — so the pass
needs some other marker to stop re-reading them, which is the stage column under a
different name.
**Consequence — a `RECONCILED` row is not evidence that a Transaction exists.** Anything
asking "which artifacts produced money?" reads `transaction_evidence`, not
`processing_stage`. Accepted, and it is the same property `EXTRACTED` already has: 244 rows
sit there having been extracted into nothing.
**Source.** Author decision, 2026-08-26.

---

## Template

```markdown
### Dn — <decision in one line, in the imperative or as a statement of fact>
**Status:** Accepted · YYYY-MM-DD · Reversibility: cheap | bounded | expensive
**Decision.** What was chosen, concretely enough to act on.
**Closes.** <open question this answers, if any>
**Why.** The reasoning. One paragraph.
**Rejected — <option>.** What lost, and the argument it lost to. Only when a real
alternative was considered.
**Consequence.** What this now forces or forbids. Include the costs accepted.
**Source.** <document and section, if the full argument lives elsewhere>
```

---

> A decision is logged when it is made, not when it is convenient. An entry that turns
> out to be wrong is superseded, never quietly deleted — the reasoning that failed is
> worth as much as the reasoning that held.
