# BillyCore — Architecture

How BillyCore is built, and why. This document answers the questions
[PRODUCT.md](PRODUCT.md) left open about shape and delivery. It does not restate the
domain — see [DOMAIN.md](DOMAIN.md) for Evidence, Claims, Transactions, and the rules
that govern them.

Every decision here is subordinate to two statements already made elsewhere:

> **Usable without AI.** BillyCore must produce useful, structured output on its own.

> **Billy may be uncertain, incomplete, or wrong. It may never be unsupported.**

---

## 1. Decisions

| # | Decision | Chosen |
|---|---|---|
| 1 | Deployment | One binary |
| 2 | Language | Go |
| 3 | Internal structure | Lightweight ports & adapters; domain has zero I/O imports |
| 4 | Processing | Database-backed staged processing; Evidence persisted before extraction |
| 5 | Interface | HTTP + JSON, versioned at `/v1` |
| 6 | Storage | SQLite (WAL) |
| 7 | Extraction | Deterministic parsers in Core; AI proposes Claims through the public API |

Sections 2–8 explain each. Section 9 records what was deliberately deferred.

---

## 2. Deployment — one binary

BillyCore runs as a single process the user starts themselves:

```
./billycore
```

It creates and owns one file, `billy.db`, in a working directory of the user's
choosing. There is no container, no orchestrator, no separate database process, and
no migration step the user has to run.

Everything BillyCore does — serving the API, polling Sources, parsing Evidence,
reconciling Transactions — happens inside that process. Background work runs on
goroutines started at boot.

### Why not an API process plus a worker process

That split solves a scaling problem BillyCore does not have. One person's financial
email is not a throughput challenge. The split also breaks the "one artifact started
locally" commitment in PRODUCT.md for no user-visible gain.

The split stays *available* because the work queue lives in the database rather than
in memory (§5). Moving extraction into a second process later is a deployment change,
not a redesign.

---

## 3. Language — Go

Chosen for the delivery shape, not for preference. A statically linked Go binary is
literally the artifact PRODUCT.md describes: one file, no runtime for the user to
install, cross-compilable to whatever the user runs.

Consequences:

- **SQLite driver.** Use a pure-Go driver (`modernc.org/sqlite`) rather than a cgo one,
  so cross-compilation stays trivial and the binary stays static.
- **Migrations are embedded.** Schema files ship inside the binary via `embed` and run
  on startup. The user never runs a migration tool.
- **The domain is plain Go.** Structs, methods, and errors. No ORM, no struct tags
  bleeding persistence concerns into `internal/domain`.

Go's weakness here is text extraction — Python has the better parsing and LLM
ecosystem. Decision 7 makes this a non-issue: the heavy extraction work is allowed to
live in another process, in another language, and reach Core through the API.

---

## 4. Internal structure — ports & adapters

```
cmd/billycore/            main; flag parsing, wiring, lifecycle
internal/domain/          Evidence, Claim, Transaction, Money, reconciliation rules
internal/app/             use cases; defines the ports it needs
internal/adapter/
  http/                   /v1 handlers, request & response shapes
  store/sqlite/           repositories, embedded migrations
  source/gmail/           connection, polling, fetching
  parser/                 per-bank deterministic parsers
```

### The one rule worth enforcing

> `internal/domain` imports nothing that performs I/O.

No `net/http`, no `database/sql`, no SQLite driver, no HTTP client, no logger that
writes anywhere. This is checkable in CI with a single import-graph assertion, and
that check is worth writing on day one. It is the mechanism that makes DOMAIN.md §9
true in code rather than in prose.

Everything below that rule is deliberately unceremonious. Ports are Go interfaces
declared in `internal/app` where they are consumed, not in a `ports` package.
Repositories return domain types directly; there is no separate persistence model and
no mapper layer until one earns its place.

### Where the invariants live

Domain invariants are enforced by constructors and methods in `internal/domain`, not
by the HTTP layer and not by database constraints. Database constraints exist as a
backstop, not as the definition.

This matters most for decision 7: a Claim arriving from BillyAgent over HTTP and a
Claim produced by an internal parser converge on the same domain constructor. There is
exactly one place where "is this a valid Claim?" is answered.

---

## 5. Processing — staged, database-backed

Ingestion does not parse. It records Evidence and stops.

```
Source fetch  ─▶  Evidence persisted        (stage: RECEIVED)
                        │
                  extraction pass           (stage: EXTRACTED)
                        │
                  reconciliation pass       (stage: RECONCILED)
```

Each stage is driven by a goroutine that claims rows from the database, does its work,
and advances the stage. Nothing is held in memory between stages.

### Why

Extraction is the part that fails: unfamiliar email layouts, a bank changing its
template, later an LLM call timing out. If extraction runs inside the fetch, a failure
loses the fetch — and the fetch may not be repeatable if the user deletes the email.

Persisting Evidence first makes the immutability invariant real rather than aspirational.
Whatever Billy observed is durable the instant it was observed, and every later attempt
to interpret it operates on a stored artifact.

### Stage is not domain state

DOMAIN.md §5 is explicit that `PARSING`, `LLM_FAILED`, and `RETRYING` are not domain
concepts. They are columns on the evidence row:

| Column | Purpose |
|---|---|
| `processing_stage` | `RECEIVED` · `EXTRACTED` · `RECONCILED` |
| `attempts` | Retry counter |
| `last_error` | Diagnostic text, never read by domain code |
| `locked_until` | Prevents two passes claiming the same row |

`internal/domain` never sees these fields. They belong to the pipeline, which is
application and infrastructure.

### Evidence identity

DOMAIN.md open question 14 — what makes two artifacts the same artifact — has to be
answered here in a minimal form, because the pipeline would otherwise duplicate
Evidence on every re-fetch. For v1:

> Evidence carries a `source_artifact_key`, unique per Source. Ingestion is an upsert
> on that key. The same email fetched twice produces one row.

For Gmail that key is the message id. This is a deduplication mechanism, not an answer
to the domain question — reconciliation still handles two *different* artifacts
describing one Transaction.

---

## 6. Interface — HTTP + JSON at `/v1`

### Why not gRPC

gRPC buys typed clients, streaming, and a schema-first contract. BillyCore has one
consumer, no streaming requirement, and a developer who benefits more from being able
to `curl` the thing while debugging a parser at 1 a.m. Language-agnostic consumption —
the actual product requirement — is satisfied by both.

The path is versioned so this stays reversible.

### Surface

| Method & path | Purpose |
|---|---|
| `POST /v1/sources/{id}/sync` | Trigger a fetch from a configured Source |
| `POST /v1/evidence` | Record a source artifact directly |
| `GET /v1/evidence/{id}` | Retrieve Evidence and its raw content |
| `GET /v1/transactions` | Query Transactions by date range, direction, status |
| `GET /v1/transactions/{id}` | One Transaction with its Claims and Evidence trail |
| `POST /v1/claims` | **Propose a Claim.** Core validates and accepts or rejects it |
| `POST /v1/reconciliations` | **Propose that two observations are one Transaction** |
| `GET /v1/reconciliations/ambiguous` | The `AMBIGUOUS` queue from DOMAIN.md §6 |
| `GET /v1/events` | Domain events since a cursor |

`GET /v1/transactions` is the endpoint the MVP success criterion is measured against —
"my last month of transactions, well classified, in a good table."

### The proposal endpoints are in v1

`POST /v1/claims` and `POST /v1/reconciliations` ship in v1, before BillyAgent exists.
This is deliberate and it is the load-bearing consequence of decision 7.

A proposal is *not* an instruction. The request body carries an interpretation and its
provenance; the response is either an accepted Claim or a rejection listing the
invariants that were violated. Core decides.

```
POST /v1/claims
  → domain constructor
  → invariants enforced
  → 201 with the Claim, or 422 with the violations
```

Specifically, a proposer cannot: create a Claim without provenance to existing
Evidence, submit Money as a float or a negative number, merge two observations that
contradict on direction or known account, or force a `MATCH` where Core computes
`AMBIGUOUS`. DOMAIN.md §6 states this as a rule; this endpoint is where the rule is
executed.

### Auth

A single bearer token, read from config at startup. The server binds to loopback by
default. This is a self-hosted single-user service; anything more elaborate is
unjustified until Billy runs somewhere with more than one person on it.

---

## 7. Storage — SQLite

One file, `billy.db`, in WAL mode with a busy timeout. No server, no credentials, no
compose file. The user backs up their financial history by copying a file.

### Concurrency

WAL permits concurrent readers with one writer. With a single process and staged
processing, the write pattern is naturally serialized: the API writes on request, each
pipeline pass writes as it advances rows. Set `busy_timeout` and keep write
transactions short. This is not a constraint you will feel at one person's volume.

### Shape

Relational, not document. Field-level confidence is a domain structure — a small fixed
set of fields each carrying a value and a confidence — so it is a table, queryable and
constrainable:

```
evidence(id, source_id, source_artifact_key, raw_content, observed_at,
         processing_stage, attempts, last_error, locked_until)

claim(id, evidence_id, state, superseded_by, created_at)
claim_field(claim_id, field, value, confidence)

transaction(id, amount_minor, currency, direction, financial_status,
            reconciliation_state, occurred_at)
transaction_evidence(transaction_id, evidence_id)
transaction_relationship(from_transaction_id, to_transaction_id, kind)

reconciliation_candidate(id, left_ref, right_ref, outcome, comparisons, decided_at)
domain_event(seq, type, payload, occurred_at)
```

Money is two columns — `amount_minor INTEGER NOT NULL` and `currency TEXT NOT NULL`.
Never a float, never a signed amount; direction is its own column, per DOMAIN.md §3.

`raw_content` is opaque text or bytes. DOMAIN.md says Evidence stores "the original raw
content, or an immutable reference to it" — a bank email is a blob, and nothing queries
inside it.

`comparisons` on a reconciliation candidate stores the per-field
`MATCH` / `MISSING` / `CONTRADICTION` results that produced the outcome. This is the
one place JSON is reasonable, because it is a diagnostic record read as a whole and
never queried by field.

### Why not Postgres

Postgres was considered and rejected. The arguments for it — JSONB for evidence
payloads, full-text search for merchant normalization — do not survive contact with
the domain. Evidence is a blob, not a document. Confidence is a table, not a JSON
column. And merchant normalization is still open question 2 in DOMAIN.md:

> Do not select infrastructure around a solution to a problem that has not been
> designed yet.

If a real requirement appears that SQLite cannot satisfy, the repositories are already
behind an interface and the migration is unpleasant but bounded.

---

## 8. Extraction — deterministic in Core, AI outside it

### v1

```
Email
  ▼
BillyCore deterministic parser
  ▼
Claim
```

Per-bank parsers live in `internal/adapter/parser`. They are ordinary Go code that
recognizes a template and produces field values with confidence. They ship with the
binary, they run offline, and they are what makes "usable without AI" true rather than
advertised.

### Later

```
Evidence
  ▼  public API
BillyAgent / AI extractor
  ▼  proposal
BillyCore
  ▼  validation
Claim
```

### Why there is no `Extractor` port with an LLM adapter inside Core

An `Extractor` interface with `RegexExtractor` and `OpenAIExtractor` implementations
is defensible under hexagonal architecture, and DOMAIN.md §9 arguably permits it
("BillyAgent *or* application / infrastructure layers"). It is still the wrong call.

It creates **two extraction pathways doing the same job** — one internal adapter and
one external proposer arriving through `POST /v1/claims` — which means two places
where the same question gets answered and two places where the invariants can drift
apart. The boundary DOMAIN.md draws is one of the cleanest things about this design,
and this weakens it for no MVP benefit.

The cost is honest and accepted: everything classified in month one is classified by
hand-written parsers. If they fall short of "well classified", the escape hatch is
`POST /v1/claims` — which is why that endpoint is in v1 rather than deferred.

---

## 9. Deliberately not decided here

These are architecture questions that a real requirement has not yet forced. Choosing
now would be inventing requirements.

- **Event delivery.** Domain events are recorded to a table and exposed at
  `GET /v1/events`. No bus, no subscriptions, no webhooks until something subscribes.
- **Merchant normalization implementation.** DOMAIN.md open question 2. The parsers
  preserve the raw descriptor; normalization has no design yet, and therefore no
  infrastructure.
- **Reconciliation time window.** DOMAIN.md open question 1. Configurable, with the
  default left unset until real Evidence shows what it should be.
- **Multi-user, hosted, or shared deployment.** Out of scope. Auth, isolation, and
  migrations would all change.
- **Structured export.** CSV, ledger formats, and anything else are consumers of
  `GET /v1/transactions`, not features of Core.

## 10. Open questions

1. Where does the binary store `billy.db` by default — working directory, or an XDG
   path? The first is more predictable, the second is more correct on Linux.
2. How are Source credentials (Gmail OAuth tokens) stored? A file next to the database,
   the database itself, or the OS keychain — each has a different backup story.
3. Should `POST /v1/evidence` be public, or is direct Evidence submission an internal
   concern of the Source adapters?
4. Does `GET /v1/events` need a durable consumer cursor, or is a client-supplied `after`
   sequence enough?
5. What happens to a Transaction whose Evidence the user deletes? DOMAIN.md open
   question 19 is a domain question; the storage-level answer — cascade, restrict, or
   tombstone — has to agree with whatever it decides.
6. Do reconciliation passes need to re-run over previously `RECONCILED` Evidence when
   new Evidence arrives, and if so, what triggers the re-examination?
