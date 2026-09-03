# BillyCore — Decisions

**Last updated:** 2026-08-28

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
| ~~What does a reconciliation candidate reference?~~ Closed by D69 | Reconciliation | DATA_MODEL.md Q1 |
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

### D45 — BillyCore runs one background pipeline worker, woken by signal and by a one-minute tick
**Status:** Accepted · 2026-08-26 · Reversibility: cheap
**Decision.** One worker goroutine in the `serve` process advances stored Evidence. It wakes
at startup, after Evidence has been successfully ingested, and every minute to find retries
whose backoff has expired. Wake signals are **coalesced**: the channel holds one pending
wake and the notifier never blocks. Each wake drains extraction and then Transaction
construction, each until a pass reports zero rows. A pass that fails is logged and does not
stop the other stage or the worker.
**Closes.** The scheduling policy ARCHITECTURE.md §5 left unspecified when it described the
stages: what runs them, how often, and what wakes them.
**Why.** Three wake sources, because there are exactly three ways work becomes eligible.
Restarting finds rows mid-pipeline and nobody to announce them, so the worker looks first.
Ingestion creates work and knows the moment it did, so it says so. A retry falling due is
the one that nothing can announce — D39's backoff is a timestamp in a column, and the
instant it passes is not an event — so the tick exists for that and is sized to
`RetryBase`, the shortest backoff there is. Coalescing is what keeps the first two from
becoming a concurrency policy: ten syncs are ten notifications and still one drain, because
a drain that has not started yet already covers everything those ten recorded.
**Rejected — extraction inside the sync request.** It is the smallest change and it makes
`POST /sync` return a number about Claims, which is what someone reading the response
wants. It also makes the response time of a sync a function of how much work it happened to
create, puts a parser on the request path where a panic is a 500 rather than a failed row,
and gives `POST /v1/evidence` — one artifact, immediate — the same problem in a worse
shape. API.md §5 promises a synchronous sync; it promises nothing about interpreting.
**Rejected — a pass per stage on its own timer.** Independent tickers would run
reconciliation before the extraction that fed it, so an artifact would take two intervals to
cross two stages for no reason. Draining in order is one line and removes the question.
**Rejected — one worker per stage, or a pool.** Both are answers to contention BillyCore
does not have: one user, one SQLite file, and a corpus that extracts in seconds. The queue
already tolerates concurrency through leases (D39), so this stays reversible — but adding
goroutines before there is a wait to justify them is how a single-file daemon acquires a
scheduler.
**Consequence — sync counts describe recording, not interpreting.** A sync answers with
Evidence created and says nothing about Claims. Whether the pipeline has caught up is a
question for the moment after it drains, and the response deliberately does not pretend to
answer it.
**Consequence — the callback is named for the fact, not the caller.** `onEvidenceAvailable`,
not `onSyncComplete`: `POST /v1/evidence` will call the same one, and so will anything else
that writes an artifact.
**Consequence — a failure is logged and retried, never escalated.** The error a pass returns
is the queue being unreachable or the context ending; a bad artifact is already recorded
against its own row and backed off. A worker that exited on one would take the pipeline down
for the life of the process over a database that was briefly locked.
**Consequence — shutdown cancels the worker before waiting for it.** Cancellation is what
lets `Extractor` and `Reconciler` release rows they claimed and never attempted, so a
restart finds them claimable at once instead of waiting out a lease. The wait shares the
30-second shutdown grace with the HTTP server rather than adding its own.
**Source.** Author decision, 2026-08-26.

### D46 — One Evidence has one active interpretation containing one or many Claims
**Status:** Accepted · 2026-08-26 · Reversibility: bounded
**Supersedes.** D38's one-ACTIVE-Claim-per-Evidence cardinality. D38's actual invariant —
one interpretation Billy currently uses, selected by a database constraint rather than a
code-path check — remains.
**Decision.** Deterministic extraction produces one complete interpretation of an
Evidence artifact. That interpretation contains one or many Claims, and every Claim still
describes exactly one financial movement. An email interpretation normally contains one
Claim; a statement interpretation contains one Claim per statement movement. At most one
complete interpretation of an Evidence artifact is ACTIVE at a time.
**Closes.** The blocker exposed when statement ingestion reached D38: one PDF is one
immutable Evidence artifact and may contain tens of movements, while
`evidence_active_claim (evidence_id PRIMARY KEY, claim_id)` permits only one of them to be
active.
**Why.** The unit received from the Source and the unit of financial meaning are not the
same thing. Evidence preserves the source artifact verbatim; a Claim states one belief
about one movement. Making either pretend to be the other loses a property BillyCore
depends on: splitting a PDF into invented Evidence weakens provenance, while putting a
whole statement into one Claim requires repeated values in a vocabulary deliberately
shaped as one amount, one direction and one event time.
**Rejected — one Claim for the complete statement.** It avoids a schema change and breaks
the Claim aggregate: `amount_minor`, `direction`, `merchant` and `occurred_at` would need
arrays, and one Claim would then produce many Transactions despite D42's one-movement
meaning.
**Rejected — independently activate Claims under `(evidence_id, row_key)`.** Smaller than
an interpretation boundary, but it makes a parser's row numbering part of identity and
allows a corrected extraction to leave a mixture of old and new rows active. A line
number is not stable when a parser begins ignoring a heading, joins a wrapped row, or
splits a row it previously misread.
**Consequence — activation is atomic at the interpretation boundary.** Extraction
validates and writes the complete Claim set, activates it, and advances the Evidence to
`EXTRACTED` in one database transaction. If any Claim fails, no partial interpretation is
active and the Evidence remains retryable. Re-extraction replaces the complete active
interpretation rather than updating Claims in place.
**Consequence — D38's table and the extraction port must change.**
`evidence_active_claim` cannot be widened with a composite primary key and called done;
the database needs an explicit interpretation boundary, and `Interpreter` and
`ClaimRepository.Save` must carry a set rather than one field map and one Claim. The
constraint selecting one active interpretation remains the authority on idempotency.
**Consequence — Transaction construction remains Claim-shaped.** D41 and D42 survive:
each ACTIVE Claim still owns one Transaction slot and produces one Transaction. The
reconciliation queue must enumerate all Claims in the active interpretation rather than
joining an Evidence row to a singular Claim.
**Open — set-level supersession must not invent row lineage.** The existing
`superseded_by_claim_id` points one old Claim at one replacement. Two interpretation sets
may have different sizes after a parser fix, so there is not necessarily an honest
one-to-one mapping. The schema slice must decide how set-level supersession is recorded
before changing that column or assigning replacement Claims by position.
**Source.** Author decision, 2026-08-26.

### D47 — An interpretation carries explicit supersession lineage
**Status:** Accepted · 2026-08-28 · Reversibility: bounded — it is a schema change
**Decision.** Migration 005 replaces `evidence_active_claim` with three tables:
`interpretations (id, evidence_id, superseded_by_interpretation_id, created_at)`,
`interpretation_claims (interpretation_id, claim_id)` as membership, and
`evidence_active_interpretation (evidence_id PRIMARY KEY, interpretation_id)`.
`superseded_by_interpretation_id` points from the old set to the new one, the same
direction `claims.superseded_by_claim_id` already reads.
**Closes.** The item D46 left open — how set-level supersession is recorded without
inventing row lineage.
**Why.** Two questions have to be answerable from the schema rather than reconstructed:
*what does Billy believe now* — `evidence_active_interpretation` — and *what did Billy
believe before* — the `superseded_by_interpretation_id` chain. Recording lineage at the
set level is honest where per-row lineage is not: two interpretations of one statement may
contain different numbers of Claims after a parser fix, and there is no truthful one-to-one
mapping between forty rows and thirty-eight. Without the pointer, an artifact accumulates
undifferentiated Claim sets whose order has to be inferred from timestamps.
**Rejected — membership as an `interpretation_id` column on `claims`.** One table fewer,
and it makes membership a property of the Claim rather than a join. It forces every Claim
to be born into a set, so `POST /v1/claims` would have to mint an interpretation id for an
outside proposer offering a single Claim. The join table keeps D38's property intact: a
`PROPOSED` Claim takes no slot anywhere and collides with nothing until it is activated.
**Rejected — per-Claim lineage alone, using the existing column.** It needs no new schema.
Investigated on 2026-08-28 and found to be a reserved seat: `Claim.SupersededByClaim` has
no call site outside `internal/domain`, and `superseded_by_claim_id` is NULL on every row
ever written, because D37 means each Claim is born `PROPOSED` and activated in the same
transaction and nothing transitions one to `SUPERSEDED`. Making it the only lineage would
require assigning replacements by position, which is exactly the invention D46 forbade.
**Consequence — `claims.superseded_by_claim_id` stays, and stays unused.** It remains in
DATA_MODEL.md §4.2 and in API.md's `supersedes` / `superseded_by`. Set-level supersession
does not write it; a future outside proposer replacing one Claim one-for-one still can.
**Consequence — the invariant survives unchanged, one level up.** The primary key on
`evidence_active_interpretation(evidence_id)` is D38's rule restated: at most one live
reading of one artifact, enforced by a constraint rather than by a code path that remembers
to check. Idempotency keeps working the way M1's ingestion does.
**Consequence — three code sites move.** `activeClaimID` (`queue.go:265`) becomes a join
returning every Claim in the active interpretation, `ClaimRepository.Save` (`claim.go:116`)
takes a Claim set and claims the interpretation slot once, and `Interpreter.Interpret`
returns a slice of field maps rather than one. Activation stays atomic at the
interpretation boundary, as D46 requires.
**Source.** Author decision, 2026-08-28.

### D48 — Re-extraction reprocesses immutable Evidence by resetting its stage
**Status:** Accepted · 2026-08-28 · Reversibility: cheap
**Decision.** Re-extraction resets `processing_stage` from `EXTRACTED` or `RECONCILED`
back to `RECEIVED`, and does nothing else to the row. `PendingExtraction` then hands the
artifact out again and the fixed parser produces a new interpretation, which supersedes the
old one under D47.
**Closes.** The gap found on 2026-08-28: there was no re-extraction path at all. A parser
fix left every already-extracted artifact holding its old Claims permanently, with no route
back short of editing the database by hand — and `ClaimRepository.Save` would have refused
the better interpretation anyway, rolling it back on the active-slot conflict.
**Why.** `processing_stage` is pipeline position, not domain state — ARCHITECTURE.md §5
says so and D44 rests on the same distinction. "Process this artifact again" is the only
thing that column exists to express, so re-extraction needs no new mechanism. Evidence
itself is untouched: the bytes are what Billy received (D7, D10, AGENTS.md §3.3), and
understanding them better is not a claim that something different arrived.
**Rejected — an extraction job or parser-version system.** A recorded parser version per
interpretation would make re-extraction selective: reprocess only the artifacts an outdated
parser touched. It is the right answer once there are several parsers changing at different
rates, and it is scope the MVP has not earned. The stage reset is reversible into it later,
because D47's lineage already records which interpretation came from which pass.
**Consequence — re-extraction is all-or-nothing per artifact set chosen by the operator.**
There is no parser-version filter, so the selection is whatever query resets the stage.
Against the present corpus a full re-extraction is 1,044 artifacts.
**Consequence — the trigger is not yet built.** Nothing exposes this. Whether it is a
`billycore reextract` subcommand, a `/v1` route, or SQL run by hand is a slice of its own,
and the constraint on it is SECURITY.md's: resetting a stage must never be reachable
without the bearer token.
**Source.** Author decision, 2026-08-28.

### D49 — A Transaction has its own lifecycle; superseded is not a reconciliation state
**Status:** Accepted · 2026-08-28 · Reversibility: bounded — it is a schema change
**Decision.** `transactions` gains `transaction_state` — `ACTIVE` or `SUPERSEDED` — and
`superseded_by_transaction_id`. When an interpretation is superseded, the Transactions
built from its Claims move to `SUPERSEDED` and point at their replacements.
`GET /v1/transactions` returns `ACTIVE` rows only.
**Closes.** What D41 deferred: *"a superseding Claim takes a free slot and builds a second
Transaction... Retiring it is a decision this slice does not take and does not need."* D48
makes it needed.
**Why.** Each column answers one question. `reconciliation_state` answers "have two
observations been determined to be the same event?"; `financial_status` answers "what
happened to the money?"; the two were deliberately kept apart, and `ReconciliationState`'s
own doc comment gives the reason. "Is this still Billy's current representation?" is a
third question and takes a third column.
**Rejected — `reconciliation_state = SUPERSEDED`.** No new column, and it reads plausibly.
It makes `SUPERSEDED` an alternative to `UNRECONCILED` and `RECONCILED`, which it is not: a
superseded Transaction either had been reconciled or had not, and that stays true after it
stops being current. It is the same collapse `ReconciliationState` was split from
`FinancialStatus` to avoid, made a second time in the same table.
**Rejected — deleting the Transaction.** The rule that governs Claims governs this:
nothing supporting a financial fact disappears because something better arrived
(DATA_MODEL.md §6, DOMAIN.md §4). A deleted Transaction also destroys the only record that
the number in last month's table used to be different.
**Consequence — the double count D41 predicted is closed.** `claim_transaction` is keyed on
`claim_id`, so a re-extraction's new Claims take free slots and build a second full set of
Transactions. Without this column, re-extracting the corpus would show every transfer
twice — the failure D41's rejected content-key option was guarding against, arriving
through a different door.
**Consequence — every Transaction read filters on `transaction_state`.** The API, the
terminal table and the web page of D32 all show `ACTIVE` only. A row's absence from that
view is not evidence it never existed, which is the same property `RECONCILED` already has
under D44.
**Open — a Transaction supported by several artifacts cannot be retired this way.** Once an
email and a statement line both support one Transaction, re-extracting the email does not
make it obsolete: the statement may still support it. The rule that generalises is closer
to *recompute the affected Transaction from the currently active Claims* rather than
*supersede one-for-one*. It does not block this entry, because D41 and D42 mean one ACTIVE
Claim owns exactly one Transaction today, so one-for-one retirement is correct for every
Transaction Billy can currently build. It has to be answered before reconciliation merges
anything.
**Source.** Author decision, 2026-08-28.

### D50 — BillyCore hardcodes the currencies it supports, with their exponents
**Status:** Accepted · 2026-08-28 · Reversibility: cheap
**Decision.** `internal/domain/currency.go` holds a map from each supported currency to
its ISO 4217 minor-unit exponent. It holds one entry today: `MXN` at 2. `Currency
.Validate` rejects a code that is not in it, and `Money.Decimal` renders an amount with
the exponent of its currency.
**Closes.** API.md open question 8 — where the minor-unit exponent per currency comes
from. The three options it named were a currency table, hardcoding the supported set, or
leaving the exponent to the consumer.
**Why.** BillyCore supports exactly one currency (D30) and has 800 stored Transactions in
it. A currency table is data to maintain and refresh for a problem nobody has: adding a
currency is a line in a map and a test. Leaving the exponent to the consumer contradicts
the success criterion, which asks for *good financial information* rather than an integer
the reader has to know how to scale.
**Rejected — ship a currency table.** The general answer, and the right one for a service
with many currencies. It buys nothing here and has to be sourced, stored and kept
current, which is a supply chain for a fact that changes about once a decade.
**Rejected — leave the exponent to the consumer.** Cheapest in Core. It moves the one
piece of knowledge that turns `100000` into `1000.00` outside the system that owns the
number, so every consumer reimplements it and one of them gets it wrong.
**Consequence — the currency vocabulary is now closed.** Before this, any three uppercase
letters validated. A parser that produces `USD` now fails at the domain boundary rather
than storing an amount nothing can render. That is the intended behaviour and it is a
narrowing: it is the same discipline `TransactionDirection` and `FinancialStatus` already
keep.
**Consequence — a weakened test was found and repaired.** `TestAddRejectsMixedCurrency`
built its second value with `NewMoney(100, "USD")` and discarded the error. Once USD
stopped validating, that call returned the zero Money and the test began asserting that
an empty currency does not add to MXN — still green, no longer about mixed currencies. It
now builds the value directly. A closed vocabulary can silently defang any test that
discards a constructor error.
**Consequence — `Money.String` and `Money.Decimal` are different renderings on purpose.**
`String` keeps the exact stored integer for a log or an error. `Decimal` is the form a
person reads. API.md question 9 warns against two amount formats in one contract; these
are not in a contract, and the wire format is unchanged.
**Open — this does not answer API.md question 9.** Whether `POST /v1/claims` accepts a
decimal string and converts it server-side is still open, and it is now cheaper to say
yes, because Core holds the exponent that such a conversion needs.
**Source.** Author decision, 2026-08-28.
### D51 — PDF text extraction shells out to `pdftotext`
**Status:** Accepted · 2026-08-28 · Reversibility: bounded — a runtime dependency and one adapter
**Decision.** BillyCore extracts text from a PDF statement by running the external
`pdftotext` executable through `os/exec`, conceptually
`exec.CommandContext(ctx, "pdftotext", "-layout", inputPath, "-")`, taking stdout as the
input to the statement parser and stderr as diagnostics. **No Go PDF parsing library, and
no Python.** The pipeline is: PDF Evidence → `pdftotext -layout` → plain text →
deterministic Go statement parser → Claim set → the existing Transaction and
reconciliation stages.
**Closes.** The open problem CONTEXT.md §3 named when M2 was scoped: Go has no standard
library for PDF text extraction, so this was "either a dependency the author approves
(SECURITY.md §11) or an external extractor posting to `POST /v1/claims`". It is neither of
those two.
**Why.** The real Nu July 2026 statement is text-based, and its transaction structure
survives text extraction well — regular purchase rows, SPEI transfers with times and
tracking keys, deposits, card payments, and other movement types all come through. The
architecture fit is the other half: D5 chose Go partly to avoid cgo, and a short-lived
external process puts PDF decoding — the most exploit-prone category of code in this
project, pointed at bytes D16 treats as hostile — outside the address space that holds a
live Gmail refresh token and the whole financial database.
**Rejected — a Go PDF parsing library.** One `go.mod` entry, everything in-process, no
runtime dependency for the user. It links the parser of hostile input into the process
that holds every secret in SECURITY.md §1, and §11 is explicit that anything linked in
inherits that access and that parsing libraries get the most scrutiny. The cgo-backed
options also contradict D5.
**Rejected — Python.** It has the strongest extraction ecosystem and it introduces a
second language, a second toolchain and a second dependency manager into a project whose
delivery shape is a single statically linked Go binary (D3, D5).
**Rejected — D11's external proposer as the PDF path.** It remains the escape hatch it was
designed to be, and it is not the MVP path. It would also need a writer for
`POST /v1/claims` that D46 removed when the unit of activation became a set, so choosing
it would add work before any statement could be read at all.
**Consequence — the boundary is bytes to text, and nothing else.** `pdftotext` performs no
financial interpretation. Every Nu-specific rule stays in BillyCore's Go parser, beside
the four email templates. A subprocess that started deciding what a row means would put
interpretation outside the system that validates it.
**Consequence — D4's "one binary" narrows.** BillyCore is still one process the user
starts, and it now expects `pdftotext` on `PATH`. It is launched only when a PDF is
extracted, never as a daemon. Poppler is not bundled in the MVP.
**Consequence — a dependency that `go.sum` cannot see.** `govulncheck` does not scan it
and SECURITY.md §11's review procedure assumes a package manager's tree. The trust
boundary moved rather than disappeared: the cost is accepted in exchange for the process
boundary, and the host's package manager owns the patching.
**Consequence — a missing `pdftotext` fails the extraction, not the startup.** BillyCore
starts, serves, and ingests email without it. The failure names the missing runtime
dependency plainly, so the user can install it.
**Consequence — the domain learns nothing about any of this.** The integration sits behind
an application port with an adapter beneath it. `internal/domain` never sees `os/exec`,
Poppler, PDFs, filesystem paths or subprocesses (D6).
**Consequence — the safety controls ship with the first line, not after it.** An execution
timeout; a bounded output size; no shell; arguments passed directly through
`exec.CommandContext`; a controlled environment; a non-zero exit status treated as an
extraction failure; and the original PDF Evidence preserved unchanged (D7, D10).
**Consequence — two earlier decisions are confirmed by the artifact.** One statement is
one immutable Evidence holding many movements, which is exactly the shape D46 restructured
the schema for. And the statement carries SPEI tracking keys, which is where D36 said the
`tracking_key` field would earn its place — it reaches only 16 of the 1,044 email
artifacts, and D42 rejected merging on it against that corpus for that reason.
**Consequence — the first slice is a probe, not a parser.** Prove the smallest vertical
path first: a real Nu PDF → `pdftotext` → deterministic rows → Claims. Test the extractor
against several real statements from different months before writing the full parser. **If
the extracted structure is not stable across months, stop and report the difference rather
than adding heuristics silently.** That instruction is part of this decision: a parser that
absorbs template drift quietly is how a table acquires rows nobody can account for.
**Open — how the PDF becomes Evidence.** `POST /v1/evidence` with a `content_type` of
`application/pdf` (D24, D27), by hand, because the statement is behind an app login and
D16 forbids Core from fetching it. The size bound on that endpoint and the shape of the
upload are not settled here.
**Source.** Author decision, 2026-08-28.

### D52 — A Source names an extraction profile, and each reading snapshots it
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — configuration and schema
**Decision.** Every configured Source names one extraction profile from a closed
vocabulary: `NU_EMAIL_V1`, `NU_STATEMENT_V1`, or `HSBC_STATEMENT_V1`. Ingestion copies
the profile onto the Evidence queue row. Each Interpretation records the profile that
produced it. A profile name follows `<ISSUER>_<DOCUMENT FAMILY>_V<n>`; its version is
BillyCore's reading contract, not the issuer's document version.
**Closes.** The dispatch question introduced by adding statement Evidence: content type
cannot select a parser when two issuers both submit `application/pdf`, and current Source
configuration cannot explain a historical Interpretation after it changes.
**Why.** Dispatch must be deterministic and auditable. The Evidence row must keep the
profile selected when the artifact arrived, so a restart drains the durable queue without
consulting mutable configuration. The Interpretation must keep the profile that made its
Claims, so re-extraction can replace a reading without rewriting its history (D47, D48).
**Rejected — infer the parser from content.** MIME type separates email from PDF but
cannot separate two statement layouts. Inspecting issuer text before dispatch makes an
unrecorded parser guess about hostile input.
**Rejected — read the current Source profile during extraction.** It avoids a column and
makes queued work change meaning when `sources.json` changes. It also cannot explain an
old Interpretation after the Source is reconfigured or removed.
**Consequence — configuration has no default.** A missing or unknown profile stops
startup with the supported vocabulary. A known profile may exist before its parser; its
Evidence stays retryable and fails extraction with a bounded diagnostic that names the
profile but not the artifact.
**Consequence — format validation precedes interpretation.** The registry checks the
declared media type and a bounded signature where one exists. A mismatch is a dispatch
failure, not an unrecognised financial template.
**Consequence — the domain Evidence remains unchanged.** A profile is configuration, not
a fact contained in the artifact. The database stores it beside immutable Evidence as
pipeline input; the domain object continues to contain only source identity, artifact
identity, content, and timestamps.
**Consequence — migration 006 backfills the live corpus.** All 1,044 existing Evidence
rows and 800 Interpretations came from the Nu Gmail Source and become `NU_EMAIL_V1`.
Future writes always state their profile explicitly.
**Source.** Author decision, 2026-08-29.

### D53 — Direct Evidence uploads are bounded JSON for configured direct Sources
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** `POST /v1/evidence` accepts one JSON object with base64-encoded
`raw_content`. The decoded artifact is limited to **10 MiB**, and the complete HTTP body
is limited to **14 MiB**. An oversized request returns `413 Payload Too Large` with
error type `payload_too_large`.
**Decision.** `source_id` must name a configured Source of type `MANUAL` or
`BANK_STATEMENT`. The Source configuration supplies `source_type` and
`extraction_profile`; the request cannot select either. The decoder accepts one object,
rejects unknown fields and trailing JSON, and requires a non-empty artifact, artifact
key, content type, and RFC 3339 observation time.
**Decision.** The first recording returns `201` with the Evidence representation. A
repeat of `(source_id, source_artifact_key)` returns the immutable existing Evidence and
`200`, regardless of the repeated body. Neither response includes `raw_content`; that
remains exclusive to `GET /v1/evidence/{id}`. Only a new row wakes the pipeline.
**Why.** JSON matches the existing `/v1` contract and avoids adding a multipart parser.
Base64 expands a 10 MiB artifact to just under 13.34 MiB, so 14 MiB bounds the decoder
while leaving room for the other fields. Ten MiB is ample for a text-based statement and
small enough that the request, decoded bytes, and immutable copy can coexist in memory.
**Rejected — let the request name its profile.** It makes parser selection a mutable
claim by the uploader and breaks D52's rule that the Source owns the reading contract.
**Rejected — accept uploads for a fetchable Source.** An upload into a Gmail Source can
forge an artifact inside the identity namespace owned by its adapter. Direct recording
belongs only to Sources configured for it.
**Consequence.** Configured Sources and fetchable sync targets are separate. A statement
Source can start without a fetcher. Asking `/sync` to fetch such a Source returns `409`,
because the Source exists but its configured kind does not support that operation.
**Source.** Author decision, 2026-08-29.

### D54 — Nu statement extraction preserves PDF coordinates
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** Nu statement extraction runs `pdftotext -bbox-layout`. The adapter returns
bounded coordinate XHTML/XML for a later deterministic Go parser. That parser will use
the standard `encoding/xml` package; BillyCore adds no Go dependency for this path.
**Supersedes.** Only D51's `-layout` to plain-text representation. D51's subprocess
boundary and every safety requirement remain in force.
**Why.** The May, June, and July 2026 probe found 548 dated rows. Plain `-layout`
misplaced five wrapped descriptions. `-bbox-layout` paired every date and amount on
identical coordinates and supplied a description candidate for every row.
**Consequence.** PDF extraction preserves layout evidence rather than flattening it.
Financial interpretation remains in BillyCore's deterministic Go statement parser, and
the original PDF Evidence remains immutable.
**Source.** Author decision, 2026-08-29.

### D55 — A Cajita movement is a Transaction between the user's own accounts
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — domain field and schema
**Decision.** A Cajita is a savings sub-account of the same user. Money goes into it and
comes out of it. BillyCore records each movement as a Transaction with its true
direction, and marks the counterparty as the user. An internal Transaction appears in
the table and is excluded from income and spending totals.
**Closes.** Whether the 61 Cajita rows in the May, June and July 2026 statements are
Transactions.
**Why.** The movement happened and Evidence describes it, so refusing to record it
discards a fact BillyCore holds. The money did not enter or leave the user's control, so
counting it as income or spending inflates both sides of a table whose purpose is to say
where money went. Recording the row and excluding it from the totals answers both.
**Rejected — a third direction, `INTERNAL`.** Direction carries the sign of unsigned
Money and has exactly two members by design (`direction.go`). A deposit into a Cajita
still leaves the spending balance, so its direction is real. Internal describes the
counterparty, not the direction.
**Rejected — do not record a Cajita movement.** It is the cheapest reading and it loses
the user's saving behaviour, which the statement states plainly and nothing else holds.
**Consequence — the domain needs a new field.** Neither Claim nor Transaction can say
that the counterparty is the user. `FieldMerchant` is the wrong place: a Cajita is not a
merchant. This is a DOMAIN.md change and belongs to the author.
**Consequence — reconciliation.** An internal movement must never reconcile with an
external one. Two Cajita movements of equal amount on one day are two real movements,
not one seen twice.
**Source.** Author, 2026-08-29: "a cajita basically means that you save that money in a
sub account, you can put there or extract from there." The author confirmed the same day
that an internal movement is excluded from income and spending totals.

### D56 — A foreign-currency purchase is recorded in the pesos that Nu settled
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** BillyCore converts no currency. A foreign purchase is recorded with the
MXN amount the statement prints in its amount column. The origin amount and the rate are
kept as Claim fields for context, and never as Money.
**Closes.** The conflict found on 2026-08-29 between D50, which closed the currency
vocabulary to MXN, and the four USD purchases in the May and July 2026 statements.
**Why.** The statement already carries the conversion. `WHOOP Compra` reads `-$522.83`
on the row, and `USD 30` with `USD 1.00 = MXN 17.4277` below it; 30 × 17.4277 = 522.83.
The peso figure is what left the account. Converting again would introduce a float and a
rounding rule for an amount the issuer already settled exactly. Money stays integer
minor units (AGENTS.md §3 rule 2) and the vocabulary stays closed at MXN (D50).
**Rejected — convert the USD amount inside BillyCore.** It computes a number the
document already states, and it can disagree with the user's own bank by a centavo.
**Rejected — a second Money in USD.** It reopens the currency vocabulary D50 closed, to
carry an amount that never moved through the account.
**Consequence — the exchange line is not a row.** It sits about 23 points below its
purchase, as a description block and an amount-column block with no date block. The row
assembler must attach it to the purchase above it. A rule of "the nearest date above"
invents a Transaction of `USD 30`; the rule of "a date and an amount on one y" leaves it
unmatched, which is correct.
**Consequence.** A purchase abroad is a peso Transaction in the table, and the origin
amount explains it without changing it.
**Source.** Author decision, 2026-08-29.

### D57 — The Interpreter port takes a context
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** `app.Interpreter` becomes
`Interpret(ctx context.Context, raw []byte)`. The use case hands over the context it
already holds. A parser that works in memory accepts the context and ignores it.
**Closes.** How a statement parser, which runs `pdftotext` outside the process, receives
the cancellation that D51 and D54 require of it.
**Why.** The extractor is `Extract(ctx, pdf)`, and its timeout, its caller cancellation
and its stop-on-oversized-output all travel in that context. The old port had nowhere to
put one, so a statement parser would have had to invent `context.Background()`: a
shutdown would no longer stop a running child, and the caller-cancellation path of the
extractor would become code that nothing reaches. The context is already present one
frame above the call, in `extractOne`.
**Rejected — extract before dispatch.** The use case runs `pdftotext` and gives the
coordinate XHTML to `Interpret` as `raw`. It keeps the port pure and it moves knowledge
of PDF into the use case, against D52's rule that the profile owns the reading contract.
It also makes `raw` something other than the Evidence bytes, which is a change of meaning
next to an immutable artifact (D7).
**Rejected — `context.Background()` inside the statement parser.** The cheapest change,
and it discards a guarantee that is built and tested.
**Consequence.** One implementation, one call site and seven test call sites changed. The
Nu email parser names the parameter `_`. `TestTheInterpreterReceivesTheCallersContext`
fails if the use case ever passes a context of its own.
**Source.** Author decision, 2026-08-29, choosing between the three options above.

### D58 — A statement field is High when two geometric facts agree
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** A value that the right column **and** a partner on the same y both confirm
is `HIGH`. A value that only its column places is `MEDIUM`. A value that carries its own
label keeps D34 unchanged.
**Closes.** How D34 applies to a document that labels nothing.
**Why.** D34 separates a labelled value from a positional one, and a statement has no
label on a date or an amount. Read literally, D34 rates the issuer's settled record
below a notification email, which is backwards. The positional case D34 guarded against
was a brittle one — "the fourth line of the body" — and a column cross-checked by a
partner is not that. The date and amount of a row sit on one y in 548 of 548 rows across
three statements. Two independent facts agree, and neither is a guess.
**Consequence.** In a statement row the date and the amount are `HIGH`, because each has
a column and a partner. The merchant is `MEDIUM`, because only its column places it. In
a SPEI detail block the tracking key and the counterparty are `HIGH` by D34, because the
document names them.
**Consequence.** Confidence now answers "how did Billy find this", not "which document
was it in". A parser that pairs a date with an amount by nearness rather than by an equal
y must not report `HIGH`.
**Source.** Author decision, 2026-08-29.

### D59 — A statement reading may be partial, and records how partial
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — schema
**Decision.** A row that no shape reads is skipped. The other rows of the statement are
written together, in one transaction, as one Interpretation. The Interpretation records
how many rows it could not read.
**Closes.** What D46's "the set is the unit" means when the set is one statement of about
180 Claims instead of one email of one.
**Why.** D46 makes a set atomic so that a half-written reading cannot exist. That is
right for an email, where the Claims describe one movement. A statement holds about 180
movements that have nothing to do with each other, and row 47 being unreadable does not
make rows 1 to 46 wrong. Discarding 179 good rows for one bad one keeps a whole month out
of the table.
**Rejected — keep D46 strict for a statement.** One odd row blocks the month. The failure
is loud, and the cost is the success criterion.
**Rejected — skip a row and say nothing.** The table under-reports and nothing shows it.
PRODUCT.md's rule is that BillyCore may be incomplete and may never be unsupported.
**Consequence — the write stays atomic.** Partial describes the reading, not the write.
The rows that were read are still one Interpretation in one transaction (D46).
**Consequence — a column.** The `interpretations` table needs the count of unread rows,
which is migration 007. A reading with a count above zero is a reading that asks to be
looked at, and re-extraction (D48) is how it is corrected.
**Source.** Author decision, 2026-08-29.

### D60 — A foreign purchase records the settled pesos only, for now
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** The statement parser records the peso amount of a foreign purchase and
nothing else. The origin amount and the exchange rate are not recorded yet.
**Closes.** D56 keeps the origin amount and the rate "as Claim fields for context", and
no name exists for either. This says the context waits.
**Why.** The peso amount is the whole of what the table needs, and it makes the row
correct today. Naming two fields is a vocabulary decision under D21, and `claim.go`
already refuses to take one in passing. Nothing yet asks BillyCore to show "you paid USD
30", so the decision has no requirement behind it.
**Consequence.** D56 stands: no conversion, the peso figure is what is recorded. Only its
context clause waits. The exchange line is still never a row.
**Consequence.** Adding the context later costs a new reading of the same immutable
Evidence, which is exactly what re-extraction is for (D48).
**Source.** Author decision, 2026-08-29.

### D61 — FieldCounterparty names who was on the other side
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — domain vocabulary
**Decision.** `FieldCounterparty` joins the Claim field vocabulary. It holds the person
or the institution on the other side of a transfer. `FieldMerchant` keeps its meaning: a
place where something was bought.
**Closes.** The tension CONTEXT.md §3.1 recorded — "transfer counterparties are person
names and CLABE entities, not merchant descriptors" — now that a statement makes it a
question of code and not of documentation.
**Why.** A SPEI detail names a person and an institution, and neither is a merchant. One
column that holds both reads wrong in the table and blurs the merchant signal DOMAIN.md
§6 depends on.
**Rejected — reuse `FieldMerchant`.** It ships sooner and it puts a person's name in a
column named merchant.
**Rejected — add the full detail vocabulary now.** `FieldCounterpartyAccount`,
`FieldConcept` and `FieldReference` are three more decisions with no requirement pushing
them. The CLABE also needs care: CONTEXT.md §3.1 warns that the counterparty's account
recorded as the user's poisons the account signal.
**Consequence.** The SPEI slice emits `FieldCounterparty` and `FieldTrackingKey` and
leaves the CLABE, the concept and the reference unread. They stay in the immutable
artifact for a later reading (D48).
**Consequence — a comment is now wrong.** `FieldTrackingKey` says a tracking key is
"never an inflow, so the two halves of a transfer can never be matched by it". That was
true of the mailbox, where 16 of 1,044 artifacts carried one. A statement carries one on
128 of 130 SPEI movements, in both directions.
**Source.** Author decision, 2026-08-29.

### D62 — An internal movement records the user as the counterparty
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — domain vocabulary
**Decision.** `FieldCounterparty` carries a reserved value that means "the user". A
movement between the user's own accounts records it. An internal movement appears in the
table and is excluded from income and spending totals (D55).
**Closes.** D55's open consequence: neither Claim nor Transaction could say that the
counterparty is the user.
**Why.** The fact belongs to the other side of the movement, and `FieldCounterparty`
(D61) is the field that describes the other side. No new field is added.
**Rejected — `FieldTransferScope` with `INTERNAL` and `EXTERNAL`.** It keeps the scope
and the counterparty as two separate facts and adds a field for a distinction that one
value can carry.
**Rejected — a boolean `FieldInternal`.** Every other field is a value and not a flag,
and a flag cannot hold a third scope later.
**Consequence — the value is a domain constant.** It is declared once in
`internal/domain/claim.go` and validated there. No parser writes the literal. A reader
that shows the counterparty as a name must know the value and must not print it raw.
**Consequence — the value must be one a statement cannot print.** A counterparty named
the same word would be read as the user. The constant is chosen so that no name Nu
prints can equal it.
**Source.** Author decision, 2026-08-29, choosing among three options.

### D63 — Dinero de respaldo is the user's own money
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** A row that reads `Retirado de tu dinero de respaldo` is an internal
movement, the same as a Cajita movement. It records the user as the counterparty (D62)
and stays out of income and spending totals (D55).
**Closes.** What the 44 `respaldo` rows in the May, June and July 2026 statements are.
**Why.** The backing money is the user's own, held aside. Moving it to the spending
balance moves nothing into or out of the user's control. Counting it as income would
report about 44 arrivals of money that was already there.
**Consequence — one shape family.** Cajita and respaldo are read together: about 105
internal movements across the three statements.
**Consequence — the cover page is not a movement.** `Dinero de respaldo` also appears on
the first page beside `su Cuenta` and `Total`, as an unsigned balance with no date. That
is a summary figure, and the row assembler must produce nothing from it.
**Source.** Author decision, 2026-08-29.

### D64 — A statement date with no time is midnight in Mexico City
**Status:** Accepted · 2026-08-29 · Reversibility: cheap
**Decision.** A statement row states a day and no time of day. The parser reads it
as 00:00:00 in `America/Mexico_City` (D29) and stores the UTC instant, so
`31 MAY 2026` becomes `2026-05-31T06:00:00.000Z`. The confidence stays `HIGH`, because
D58 rates the day, which the column and its partner both place.
**Closes.** The gap found on 2026-08-29 while writing the `Compra` shape.
`parser.Wall` carries `HasTime` and says "midnight is a real time; absence is not
midnight", so the date handling deliberately refused to choose. Every existing caller
either had an `Hora:` line or emitted no `occurred_at` at all (D33).
**Why.** The day is the only fact the row states, and midnight in the user's own zone is
the reading that keeps that day right for the reader the table is for. Mexico City is
UTC-6, so reading the wall value as UTC would move each purchase to the day before.
**Rejected — midday in Mexico City.** It survives a careless re-render in any zone from
UTC-12 to UTC+11. It loses because it invents a time of day no statement ever prints,
and it buys robustness against a consumer that is BillyCore's own.
**Rejected — emit no `occurred_at` and let the fallback fire.** Strictly honest, and
wrong here: DATA_MODEL.md §4.5 falls back to the artifact's `observed_at`, which is when
the PDF was uploaded, so about 180 rows of one month would collapse onto one date and
the row date the document does state would be discarded.
**Consequence.** The time of day is a convention and not a reading. A statement
Transaction sorts before every email Transaction of the same day, and reconciliation
between the two Sources must compare days rather than instants.
**Source.** Author decision, 2026-08-29, choosing between the three options above.


### D65 — The reserved counterparty value is `urn:billy:self`
**Status:** Accepted · 2026-08-29 · Reversibility: bounded — it is a stored value
**Decision.** `domain.CounterpartySelf` is the string `urn:billy:self`. It is declared
once in `internal/domain/claim.go`. `FieldCounterparty` accepts it, and rejects every
other value that starts with `urn:billy:`, which is the namespace BillyCore owns.
**Closes.** D62 requires the value to be "one a statement cannot print" and does not name
it. Choosing a literal in passing inside a parser is what D21 forbids, so it is chosen
here.
**Why.** A URN is not a name. A statement prints merchant descriptors, person names and
institutions, and none of them is a colon-separated URN in a namespace that belongs to
BillyCore. The namespace also makes room for a second reserved value later without a
second decision about its shape.
**Rejected — `__SELF__`.** Shorter, and it reads as a placeholder. It loses because it
says nothing about who reserved it, and because underscore-wrapped words are a convention
other systems also use, so a value arriving from a future Source could collide.
**Rejected — an empty counterparty for an internal movement.** It costs nothing to write
and it is the one thing DOMAIN.md §7 forbids: absence has to keep meaning "Billy has no
belief", and an internal movement is a belief.
**Consequence — the namespace is validated, not only the value.** A near miss such as
`urn:billy:sef` is rejected at the domain boundary rather than stored as a counterparty
that reads like a person. This is the same narrowing D50 made for currency.
**Consequence — a reader must translate it.** `billycore tx` and the web page show the
user, never the raw value. Nothing shows a counterparty yet, so nothing is broken today.
**Source.** Agent implementation of D61 and D62, 2026-08-29. Overrule the literal by
superseding this entry; it is one constant and one test.


---

### D66 — A Transaction preserves the counterparty; the tracking key stays a Claim field
**Status:** Accepted · 2026-08-30 · Reversibility: bounded — a schema column
**Decision.** `transactions` gains a nullable `counterparty` column (migration
008), and reconciliation copies `FieldCounterparty` from the Claim onto the
Transaction. The tracking key gains no such column: it stays a Claim field and a
reader reaches it from a Transaction through `claim_transaction`.
**Closes.** D61's open consequence — reconciliation could accept a counterparty
Claim and then discard it, so a SPEI or an internal movement would lose the one
field that says who was on the other side.
**Why.** The counterparty is a fact about the movement, and the Transaction is
what a reader sees; a value that reaches the read model and stops there is a
value Billy read and threw away. The tracking key is different: DATA_MODEL.md
§4.5 writes the Transaction columns down and lists no tracking key, D42 already
refused to use it at build time, and the value is not lost — `claim_fields`
keeps it and `claim_transaction` joins back to it.
**Rejected — a `tracking_key` column on `transactions`.** It reads well and it
duplicates a Claim field the schema deliberately does not lift, for a
reconciliation use that no code makes yet. When reconciliation needs it, it
reads the Claim.
**Consequence.** `domain.Transaction` carries `Counterparty`, validated against
the reserved namespace in the aggregate itself (D65), because a Transaction is a
root of its own and does not trust the Claim it came from. The API and the
`billycore tx` table now show it (D67).
**Source.** Author decision, 2026-08-30.

### D67 — Income and spending totals exclude internal movements
**Status:** Accepted · 2026-08-30 · Reversibility: cheap
**Decision.** A totals read model sums income and spending over the ACTIVE
Transactions a query selects and leaves the internal movement out of both,
matching on the reserved counterparty value (D62, D65). It is exposed at
`GET /v1/transactions/summary` and shown as a footer under the `billycore tx`
table. Each Transaction in the list also carries `counterparty` and a derived
`internal` boolean.
**Closes.** D55's requirement that an internal movement is excluded from income
and spending, which had no reader to enforce it: no totals layer existed.
**Why.** The money did not enter or leave the user's control, so counting a
Cajita or a `respaldo` movement as income or spending inflates both sides of the
table whose purpose is to say where money went. The row stays visible and only
the totals drop it, which is exactly what D55 asked for. The exclusion lives in
the read model, because that is where a total is computed; the criterion is the
domain constant, passed as a query parameter, so no literal `urn:billy:self`
lives in the SQL.
**Rejected — sum in the CLI over the fetched page.** Smaller, and wrong past the
page limit: a window of more than 200 rows would under-count. The server sums
the whole filtered set.
**Rejected — carry only the raw counterparty on the wire.** A machine client
would then hardcode the URN to know a movement is internal. The `internal`
boolean says it once, and the human readers still translate the value (D65).
**Consequence.** The footer says how many internal movements it excluded, so the
omission is visible rather than silent (PRODUCT.md). Totals are single-currency,
which BillyCore already is (D50).
**Source.** Author decision, 2026-08-30.

### D68 — The Nu statement reads nine more shapes
**Status:** Accepted · 2026-08-30 · Reversibility: cheap
**Decision.** The statement parser reads nine further row shapes. Direction is
the sign of the amount column in every case, as for `Compra`.

| Shape | Direction | Party |
|---|---|---|
| `Pago a tu tarjeta de crédito Nu` | OUTFLOW | none, external |
| `<merchant> Devolución` | INFLOW | merchant |
| `<merchant> Ajuste realizado` | INFLOW | merchant |
| `Bonificación por beneficio de Nu` | INFLOW | none |
| `Compensación de retraso SPEI` | INFLOW | none |
| `Pago de servicio - <merchant>` | OUTFLOW | merchant |
| `Cajero <operator> Retiro de efectivo` | OUTFLOW | counterparty = operator |
| `Depósito en punto de venta` | INFLOW | none |
| `Descongelamos saldo de tu Cajita: <name>` | INFLOW | self (internal) |

**Closes.** The 42 genuinely-unread statement rows measured under D59, less the
mirror rows and one balance-summary line.
**Why.** Each shape names its own kind in the description column, so a
deterministic rule reads it with no new vocabulary: existing fields carry the
amount, the direction, the date and the party. A party the column places is
MEDIUM (D58); `self` is HIGH, because it is a fact about the shape and not a
reading (D62).
**Rejected — model the refund now.** A `Devolución` and an `Ajuste` relate to an
earlier purchase, and that relationship is real. DATA_MODEL.md §9 leaves
relationships open, and a refund is recorded as a plain inflow without it. The
link waits for that decision.
**The two calls that were judgements.** A card payment is external: the Nu
credit card is its own product and its own statement, not one of the debit-side
accounts D62 covers. An ATM operator is an institution, so it is the
counterparty and not a merchant, and a cash withdrawal is an outflow — where the
cash then goes is unknown, and it is not counted as internal.
**Consequence.** Unread drops from 126 to 85 across the three statements: 84
internal-mirror rows, deliberately unread (D55), and one Cajita balance-summary
line, correctly furniture. The redacted fixtures do not carry these Spanish
words, so the tests are synthetic and the counts were validated against the real
statements.
**Source.** Author decisions on the shape matrix, 2026-08-30.

### D69 — A reconciliation candidate references two Transactions
**Status:** Accepted · 2026-08-31 · Reversibility: bounded — a schema table
**Decision.** `reconciliation_candidate.left_ref` and `right_ref` are
`transactions.id`. A candidate pairs two Transactions and records a status —
`MATCH`, `NO_MATCH` or `AMBIGUOUS` (DOMAIN.md §6). The table and its foreign keys
are added by a migration when reconciliation is built.
**Closes.** DATA_MODEL.md Q1, open since the schema was written: the domain
described reconciliation between Claims or Evidence, and the API used Transaction
ids, and nothing chose. No generic untyped reference was added as a workaround.
**Why.** The Transaction is Billy's representation of one financial event, and it
carries every signal DOMAIN.md §6 compares: amount, direction, time, counterparty,
status, and the tracking key through its Claim. The rest of the system already
speaks in Transactions — D42 builds one for each Claim, born `UNRECONCILED`;
DOMAIN.md §8's `TransactionReconciled` names a "surviving transactionId"; D49
reserved `superseded_by_transaction_id`; the API uses Transaction ids throughout.
Pointing a candidate at anything else makes reconciliation translate back to these
values before it can compare them.
**Rejected — Claim ids.** More faithful to "a Claim is one observation", and it
fights the Transaction-centric API and needs a Claim-to-values path the Transaction
already is.
**Rejected — Evidence ids.** One statement PDF is one Evidence with about 180
movements, so a pair of Evidence ids cannot say which movement matched. Wrong
granularity.
**Consequence — one decision remains before the code.** What a `MATCH` does to the
two Transactions is not settled here: whether one survives and the other is retired
(`transaction_state` SUPERSEDED, `superseded_by_transaction_id` the survivor, the
column D49 reserved), or both stay and are linked. DOMAIN.md §8 and D49 lean toward
the merge, and it is the next entry to write.
**Source.** Author decision, 2026-08-31.

### D70 — A MATCH merges: one Transaction survives, the other is superseded
**Status:** Accepted · 2026-08-31 · Reversibility: bounded — it writes two reserved columns
**Decision.** When reconciliation finds two Transactions are the same event, the
candidate is recorded `MATCH` and the two are merged. One survives; the other is
retired with `transaction_state = SUPERSEDED` and `superseded_by_transaction_id`
pointing at the survivor (the columns D49 reserved and left unwritten). The
survivor becomes `RECONCILED` and its provenance is the **union** of both
Transactions' Evidence, so the two-source fact is not lost. The survivor is the
Transaction with the smaller id — the canonical `left_ref` of the pair (D69).
**Closes.** The one decision D69 left: what a `MATCH` does to the two
Transactions — merge one-for-one, or keep both and link.
**Why.** A transfer seen in a Nu email and in the bank statement is one movement,
and a table that shows it twice is wrong. DOMAIN.md §8's `TransactionReconciled`
already names a "surviving transactionId", and D49 already reserved the retirement
columns for exactly this, so merge is the reading the whole design leans toward.
The survivor keeps both Evidence ids because provenance is the one thing a merge
must never drop: the fact that two Sources agree is stronger than either alone.
Smaller-id-survives is arbitrary but deterministic, and it makes a re-run reach
the same survivor — nothing here depends on which Source won.
**Rejected — keep both Transactions and only link them.** It leaves the double
count in the list and the totals, which is the problem reconciliation exists to
remove. A link is what a refund relationship needs (DATA_MODEL.md §9), not what a
same-event match needs.
**Rejected — a new merged Transaction that supersedes both.** DOMAIN.md §6
question 20 raises it. It needs a third id and a rule for the merged fields, and
one-for-one retirement is enough while one ACTIVE Claim owns one Transaction (D42,
D49). It waits until a Transaction is supported by several artifacts.
**Consequence — the merge is idempotent by the candidate's UNIQUE pair.** A
re-run finds the pair already recorded, writes nothing, and the loser is no
longer ACTIVE to be re-selected. The constraint enforces it, not a code path.
**Consequence — a superseded Transaction leaves the list and the totals and
stays in history.** `GET /v1/transactions` already filters to ACTIVE (D49), so no
reader changes. The row and its `superseded_by` pointer keep the audit trail
(DATA_MODEL.md §6).
**Source.** Author task, 2026-08-31; DOMAIN.md §6, §8; builds on D49, D69.

### D71 — Weak-signal reconciliation never auto-merges
**Status:** Accepted · 2026-09-01 · Reversibility: easy — it only writes candidates
**Decision.** A pair with no shared tracking key is compared on the weaker signals
(DOMAIN.md §6). It reaches `NO_MATCH` on a contradiction the domain has already
decided, and `AMBIGUOUS` otherwise. It never reaches `MATCH`. Only the exact
tracking key (D36, slice 2) merges two Transactions; weak signals record a
candidate and merge nothing, which is the critical rule that `AMBIGUOUS` is never
auto-merged (DOMAIN.md §6).
**Why.** DOMAIN.md §6 leaves two signals undecided — the time window ("not yet
decided") and the amount tolerance ("does not invent tolerances") — and gives
merchant normalization to BillyAgent, not BillyCore. A safe `MATCH` needs one of
those, so BillyCore cannot reach `MATCH` from weak signals without inventing a
number the domain has withheld (D21). `AMBIGUOUS` is the honest outcome: the pair
looks alike but nothing proves it, so it waits for new Evidence, BillyAgent, or a
human.
**Consequence — the block is exact, so it invents no number.** The pass groups
ACTIVE UNRECONCILED Transactions by exact `(currency, amount_minor, direction)`
and compares only within a block. Exact equality is not a tolerance. Opposite
directions and different amounts fall in different blocks and never pair, so no
candidate is written for a pair that plainly is not the same event.
**Consequence — a Transaction without Money is not weak-paired.** With no amount
and no tracking key there is no signal to block on, so pairing it is noise. It
stays UNRECONCILED until Evidence gives it an amount or a key.
**Consequence — the one weak `NO_MATCH` is internal against external (D55).**
Within a block direction, amount and currency already agree, so the only decided
contradiction left is an internal movement against an external one. Every other
in-block pair is `AMBIGUOUS`. A differing known account is left `AMBIGUOUS`, not
`NO_MATCH`: "strongly contradict" (DOMAIN.md §6 signal 4) is not yet a rule.
**Consequence — pairs are the star of a block, smallest id to each other.** It
bounds the writes to one for each look-alike, not one for every pair, and it is
enough to flag the block as ambiguous. A full resolution is BillyAgent's.
**Rejected — invent a time window and an amount tolerance now.** It is the two
open questions DOMAIN.md §6 names, and guessing them is exactly what D21 forbids.
They wait for an author decision, and then weak signals can reach `MATCH`.
**Source.** Author task, 2026-09-01; DOMAIN.md §6; builds on D55, D69, D70.

### D72 — A weak pair merges on an exact composite key, gated by uniqueness and two Sources
**Status:** Accepted · 2026-09-01 · Reversibility: bounded — it merges Transactions, like D70
**Decision.** A pair with no shared tracking key merges (`MATCH`) when all of these
hold: equal amount, currency and direction; equal party after trimming and
casefolding only, where the party is the merchant or, where a Source leaves that
empty, the counterparty; the same calendar day in a fixed UTC−6; the two
Transactions come from different Sources; and the composite tuple identifies
exactly one Transaction on each Source. Any collision, any same-Source pair, and
any tuple that is not unique on both sides stays `AMBIGUOUS` and does not merge.
This partly answers the open questions D71 held — weak signals can now reach
`MATCH`, but only through an exact composite key, never a fuzzy tolerance or a
time window.
**Consequence — the party reads two fields.** A Nu email names the other party in
`merchant`; a statement names it in `counterparty`. The composite party reads
merchant first, then counterparty, so the field a Source happens to use does not
hide the identity the two agree on. On the June data this is the difference
between zero merges and 34: with merchant alone every statement row has an empty
party and cannot match.
**Closes.** Half of what D71 left open: what lets a weak pair merge. The tracking
key (D36) is one exact key of a movement; this is a second, weaker one.
**Why.** June proves the need and the risk together. The Nu emails of June carry
no `Clave de rastreo` — Nu added it to email only near 2026-07-23 — so a June
statement cannot tracking-key-merge with its emails, and every match must come
from the weaker signals. But amount and direction collide in June (three distinct
$500 outflows), so a merge on those alone would collapse real records. The four
guards make the composite safe: **uniqueness** stops a collision from guessing a
pair; **two Sources** keeps two real same-day payments from one Source apart,
because reconciliation means one movement seen twice, not two movements that look
alike; **exact merchant** keeps normalization in BillyAgent, not BillyCore
(DOMAIN.md §6); **fixed UTC−6** compares the day the emails (UTC) and the
statement (local) actually share — the Plomero transfer is `00:22Z`, which is
`18:22` the day before in Mexico City, so a raw compare would split it.
**Consequence — the survivor and the audit trail are D70's.** One survives, the
other is `SUPERSEDED`, provenance is the union, and the event records the merge.
Only the basis differs: `composite` rather than `tracking_key`.
**Consequence — UTC−6 is fixed, not loaded.** Mexico abolished DST in 2022, so
Mexico City is UTC−6 all year. A fixed offset needs no tzdata and no dependency
(the task forbids one). If Mexico restores DST, the day compare is off by the
offset for the affected hours, and the offset becomes a table.
**Rejected — a time window and an amount tolerance.** DOMAIN.md §6 defers both,
and D21 forbids guessing them. The composite key needs neither: it is exact
equality on every field, and the day is a calendar day, not a ± window.
**Rejected — merge any unique cross-Source amount+direction pair without
merchant.** Too weak: two unrelated same-day, same-amount transfers to different
people would merge. Merchant equality is the cheap guard that makes the tuple a
plausible identity, and BillyAgent widens it later.
**Source.** Author decision, 2026-09-01; DOMAIN.md §6; builds on D55, D69, D70, D71.

### D73 — Core serves the web page itself, unauthenticated, at `/`; the browser holds the token
**Status:** Accepted · 2026-09-02 · Reversibility: cheap
**Decision.** `billycore serve` serves one static HTML page from the binary at
`GET /`, outside `/v1` and without the bearer token, the way `/healthz` is. The page
carries no financial data. It reads the token the user pastes into a field, keeps it in
the browser tab only, and calls `GET /v1/transactions` and
`GET /v1/transactions/summary` with it. The page is one file with inline style and
script, embedded with `go:embed`; it renders every value through the DOM as text, never
as markup, and its `Content-Security-Policy` forbids every external reference.
**Closes.** The open question D32 left: does Core serve the page, or is it a file the
user opens?
**Why.** A `file://` page cannot reach `http://127.0.0.1:8787` — the browser blocks the
cross-origin call, and there is no second server to serve it same-origin. Serving from
the binary is the only version that works, which D32 already saw. The token then has to
reach the browser; a pasted field kept in the tab is the least friction that does not
write the secret to disk.
**Consequence — the page is a thin renderer, not a second API.** It computes no
financial fact: the totals come from `GET /v1/transactions/summary`, and the page only
formats minor units for display, as `billycore tx` already does (D32).
**Consequence — a viewing surface renders values derived from hostile input.** A crafted
email names a counterparty, and that string reaches the page. The page writes it with
`textContent` and never `innerHTML`, and the CSP blocks script and remote loads, so a
name that carries markup is shown, not run (SECURITY.md §7 reasoning, one layer out).
**Consequence — serving `/` unauthenticated leaks nothing.** The page is a static
template with no row in it; every byte of financial data still crosses the token
boundary on the `/v1` calls the browser makes (SECURITY.md §4).
**Rejected — a file the user opens.** It needs no route, but the browser's same-origin
rule then blocks the API call, and working around that means CORS on a loopback
financial API — more surface to defend the weaker option.
**Source.** Author task, 2026-09-02; builds on D32, D18, SECURITY.md §4, §7.

### D74 — An HSBC ledger row is the Claim; the SPEI annex enriches it
**Status:** Accepted · 2026-09-02 · Reversibility: bounded — it changes which rows become Transactions
**Decision.** On an HSBC `Cuenta Flexible Simple` statement, each row of
`DETALLE MOVIMIENTOS` is one Claim. Cargo is `OUTFLOW` and abono is `INFLOW`.
The SPEI Enviados / Recibidos annex is not a second Claim. It enriches the
ledger row that shares its amount, calendar day and direction, and only when
that triple identifies exactly one ledger row on that statement. The annex
supplies `tracking_key` and the clock for `occurred_at`. It does not replace
the ledger party: the beneficiary on an outbound SPEI is often the user.
**Closes.** The two-table identity question the June and July 2026 PDFs posed.
**Why.** Two Claims from one Evidence become two Transactions (D42, D46), and
the Matcher will not collapse them: D72 requires two Sources. The ledger is the
account movement, including payroll and card rows that the annex never lists.
The annex is where `Clave de rastreo` lives, which is what merges with Nu email.
**Rejected — the annex row is the Claim.** It drops every non-SPEI movement
and still needs the ledger for card purchases and `NETNM DEPOSITO DE NOMINA`.
**Consequence — a collision stays unenriched.** Three same-day `CGO` outflows
of $1,000 keep their ledger fields and receive no key. That is D59, not a guess.
**Consequence — the day-of-month takes the statement period.** The ledger
prints `22` and the cover prints `09/06/2026 al 30/06/2026`. Midnight in
Mexico City remains the clock until an annex hit supplies one (D29, D64).
**Source.** Author decision, 2026-09-02, on the two HSBC PDFs.

### D75 — A SPEI between two of the user's accounts is neither earned nor spent
**Status:** Accepted · 2026-09-02 · Reversibility: bounded — it changes income and spending totals
**Decision.** When two ACTIVE Transactions share a tracking key, have opposite
direction, and come from two Sources, they are one SPEI between two of the
user's accounts. Each account keeps its row. Both are excluded from income and
spending, and the API reports `internal: true`, the same as a Cajita movement
(D55, D62). The pair is not merged (D70): a merge would pick one direction and
erase one ledger. `HSB` and `HSBC` are one key: Nu still prints the annex wrap
that D74 already folds on the HSBC side.
**Closes.** The household totals that double-counted Flex→Nu transfers of wage.
**Why.** Payroll is earned once, when it arrives. Sending it to another account
the user holds is the same money. Opposite direction is what DOMAIN.md §6 uses
to refuse a merge of two views of one event; here it is the signal that the two
rows are the two sides of a transfer, not a contradiction.
**Rejected — merge them.** One Transaction cannot be an outflow on Flex and an
inflow on Nu. The account ledgers would lie.
**Rejected — amount and calendar day without a tracking key.** Collisions of
$1,000 CGO exist on the June statement (D59). Guessing the pair is D21.
**Consequence — a transfer without a shared key still looks like earned or
spent.** Three same-day $1,000 Flex outflows, and Nu inflows whose clave Nu
stored as `HSB…` only after the fold, are the cases this rule covers. A SPEI
that Nu labelled with a key Flex never received stays income until the other
Source states the same key.
**Source.** Author, 2026-09-02, on the combined June–July HSBC and Nu statements.

### D76 — A deposit the user sent to themselves is not earned
**Status:** Accepted · 2026-09-02 · Reversibility: bounded — it changes income and spending totals
**Decision.** Once D75 has proved a name is the user — it is the counterparty on the
inflow side of a keyed SPEI between two Sources — every other ACTIVE inflow with that
counterparty is the user moving money, not income. The API reports `internal: true`.
The other ledger (the Flex outflow) is excluded only when the amount and UTC calendar
day (±1) match exactly one such inflow and exactly one outflow across Sources.
**Closes.** Wage SPEI that Nu labelled `ALEJANDRO DE JESUS AYALA DIAZ` still counting
as earned when Flex never stored the clave.
**Why.** Payroll is earned once, when it arrives. Sending it to another account the
user holds does not earn it again. The name is evidence the sender is the user; it is
not a guess about amount and day. D75 already identified that name from a keyed pair.
**Rejected — amount and calendar day as the primary rule.** Unchanged from D75 / D59:
three $1,000 CGO on the same day collide. Those Flex outflows stay spending until a
key exists. The Nu inflows of the same name still drop out of earned.
**Consequence — net falls when the clave is missing on one side.** The Nu deposit is
no longer earned; the unmatched Flex cargo is still spent. That is honest. Guessing
which of three $1,000 cargos is the SPEI is D21.
**Source.** Author, 2026-09-02, on wage sent Flex → Nu.

### D77 — The Klar Principal ledger is the Claim; interest is skipped
**Status:** Accepted · 2026-09-03 · Reversibility: cheap — it is one parser
**Decision.** `KLAR_STATEMENT_V1` reads the Cuenta Principal table. Cargo is
OUTFLOW, abono is INFLOW, MXN, midnight in Mexico City (D29, D64). A card mask
(`************2099`) marks a merchant; every other concept is a counterparty.
The document prints no tracking key. Rows outside the cover period are skipped.
**Closes.** Klar as a third Source of BANK_STATEMENT, so household identity can
see Nu → Klar and Flex → Klar instead of treating `klar` as a merchant.
**Why.** The June cover states 41 movements, $9,214.00 in, $9,228.34 out. The
July cover states 48, $19,107.00 in, $19,024.39 out. Those figures are the
ledger, not the interest table.
**Rejected — read weekly interest as income.** Rendimiento is real, and a
contador would count it. It lives in a second table with $0.00 cargos. Parsing
it now mixes Principal with a pot that the cover already splits as Ganancias.
It waits until household identity has a place for interest.
**Consequence — no clave.** D75 cannot pair a Klar row by tracking key. Unique
amount and day across Sources is still D76's secondary join, and only after a
keyed SPEI has proved a name.
**Source.** Author, 2026-09-03, on the June and July Klar PDFs.

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
