# BillyCore — Decisions

**Last updated:** 2026-08-23

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
