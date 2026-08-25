# BillyCore — Context

**Last updated:** 2026-08-25

Orientation for anyone — human or agent — starting work on BillyCore. `docs/` describes
the system as designed. This file describes where the project actually *is*, which is a
different and much shorter story.

Read this first, then [docs/PRODUCT.md](../docs/PRODUCT.md).

---

## 1. State: M1 is done — real Evidence, from the real mailbox

The pipeline exists as far as Evidence, and stops there deliberately.

On **2026-08-25** `POST /v1/sources/gmail_primary/sync` ran twice against the live
mailbox:

| Run | Discovered | Created | Skipped | Wall clock |
|---|---|---|---|---|
| First | 1,044 | 1,044 | 0 | 3m 36s |
| Second | 1,044 | 0 | 1,044 | **1.1s** |

The second line is the milestone. Ingestion is idempotent on the Gmail message id, and
it is idempotent because `UNIQUE (source_id, source_reference)` says so — not because
the code remembered to check. `~/.billy/billy.db` now holds **1,044 Evidence rows and
1,044 `EvidenceIngested` events**: 51 MB of verbatim RFC 822 reaching back to
2023-08-23, every row at stage `RECEIVED`, every row with its content.

What runs today: `billycore auth`, `billycore peek`, and `billycore serve` — `GET
/healthz`, `POST /v1/sources/{id}/sync`, and `GET /v1/evidence/{id}`, the last two behind
a bearer token compared in constant time.

What does not exist: **no Claim, no parser, no Transaction**, no reconciliation, no AI,
no HSBC, and no way to see any of this that is not `curl | jq`. Nothing in that mailbox
has been *interpreted*. Evidence is bytes Billy is certain it received; it is not yet a
single financial fact, and the success criterion in §2 is about facts.

`docs/` is no longer entirely unvalidated — the ingestion half has met real email, and
§3.1 records where the design was already wrong about it. Everything from Claims onward
remains a well-reasoned hypothesis that has never run.

### Code

| Path | Lines | State |
|---|---|---|
| `go.mod` | — | Go 1.26.2, one direct dependency: `modernc.org/sqlite` (D5) |
| `cmd/billycore/main.go` | 338 | `serve`, `auth`, `peek`; data dir, token, bind-address checks |
| `internal/domain/` | 259 | `Money`, `Currency`, `Evidence`, and the D6 import-graph assertion |
| `internal/app/ports.go` | 74 | `EvidenceRepository`, `SourceFetcher`, `Artifact` |
| `internal/app/ingest.go` | 132 | Fetch → Evidence → stop. No clock, no randomness, no I/O of its own |
| `internal/adapter/store/sqlite/` | 451 | Open, pragmas, migrations, Evidence repository |
| `internal/adapter/source/gmail/` | 282 + OAuth | `ListIDs`, `GetMetadata`, `GetRaw`, `Fetcher` |
| `internal/adapter/api/` | 377 | Error envelope, bearer auth, the two `/v1` routes |
| `internal/adapter/config/sources.go` | 92 | `sources.json` (D26) |
| `internal/id/uuid.go` | 38 | UUID v4 from `crypto/rand`; outside the domain because randomness is I/O |
| **Total** | **≈2,410 code · 1,878 test** | `make check` green, and green under `-race` |

The ratio of design prose to code is no longer 600:1. It is roughly 2:1, which is the
first time this table has been worth reading.

### Documents

| Document | State |
|---|---|
| `docs/PRODUCT.md` | Written |
| `docs/ARCHITECTURE.md` | Written |
| `docs/DOMAIN.md` | Written · worked example does not match the mailbox (§3.1) |
| `docs/DATA_MODEL.md` | Written · §4.1 is one column behind the schema (D24) |
| `docs/API.md` | Written · §5 and §6 now have running implementations |
| `docs/SECURITY.md` | Written |
| `ai/CONTEXT.md`, `ai/DECISIONS.md` | Written · D1–D26 |
| `ai/CONVENTIONS.md`, `ai/CONSTRAINTS.md`, `ai/WORKFLOW.md`, `ai/GLOSSARY.md` | **Empty** |
| BillySat, BillyAgent | Docs-only scaffolds, no code. Do not start them. |

---

## 2. The clock

| | |
|---|---|
| Started | 2026-08-22 |
| MVP target | 2026-09-22 — **a target, no longer hard** (D28) |
| Realistic landing | early-to-mid October |
| Budget | 2 h/weekday + 4 h Saturday + 4 h Sunday ≈ **18 h/week** |
| Remaining to the target | ≈ 72 hours |
| Estimated to finish the scope | **≈ 80–90 hours** |

Those last two lines are the most important fact in this document, and they do not
agree. D27 put bank statements and reconciliation into the MVP; D28 answered the
collision by moving the date rather than cutting the scope. The number to watch is no
longer "hours left" but the gap between the two.

The deadline having moved once, the honest risk is that it stops constraining anything.
What holds it in place is that D27 wrote the scope down: the date moves for *that* list,
and adding to the list is a new decision, not an adjustment.

The success criterion has not moved (PRODUCT.md):

> I can see my last month of transactions, well classified, in a good table with good
> financial information.

**Nothing that does not serve that sentence gets built.** That rule survives D28 intact
— it applies with more force now, not less, because the scope grew rather than the
discipline loosening.

---

## 3. Next step

**Read the 1,044 emails Billy now holds, and turn four templates into Claims.**

The first vertical slice, in order — M1 is closed:

1. ~~`go mod init`, one binary that starts and serves `/healthz`~~ — **done**
2. ~~SQLite open, embedded migration, `evidence` table~~ — **done**, migration 001
3. ~~Gmail OAuth — read-only scopes, token to `credentials.json` at `0600`~~ — **done**
4. ~~Fetch → persist Evidence at stage `RECEIVED`, stop there~~ — **done**, 1,044 rows
5. ~~Look at real Nubank and HSBC emails~~ — **done**, and §3.1 is what they said

M1 was Gmail → Evidence → SQLite, idempotent on the Gmail message id: no Claims, no
transaction extraction, no reconciliation, no AI, no HSBC. All of it now exists, runs
against the live mailbox, and is provably idempotent without a network.

**M2 is where the design stops being about plumbing**, and D27 made it bigger than
parsers. The scope is now three things that have to land together:

1. **Email → Transactions.** Per-template parsers for the Nu templates
   (`internal/adapter/parser`, D11, D16), Claims with field-level confidence, and the
   `EXTRACTED` stage that produces Transactions. ~45 h.
2. **Statements.** Intake by hand through `POST /v1/evidence` — measured 2026-08-25, the
   statement emails carry no PDF and BillyCore may not follow the links to fetch one
   (D16, D27). PDF text extraction is the open problem: Go has no standard library for
   it, so this is either a dependency the author approves (SECURITY.md §11) or an
   external extractor posting to `POST /v1/claims`, which is the escape hatch D11
   designed for exactly this. ~25 h.
3. **Reconciliation.** DOMAIN.md §6's six signals, and the transfers that appear in both
   Sources collapsing into one Transaction. ~20 h, and it drags six open questions onto
   the path with it.

**The order matters more than the estimates.** 1 before 3, because reconciliation cannot
be tested until there are two kinds of Transaction to reconcile — and 2 before 3 for the
same reason. Statement parsing is the piece most likely to blow its estimate, and it is
the only one whose input format has never been seen.

The criterion has not moved: a table of last month's transactions. From email that is
**37 rows** — 22 inflows, 15 outflows, measured, not estimated. The statement adds the
card purchases that no email contains, which is the difference between a table of money
moving and a table of what was bought.

---

## 3.1 What the real data says — measured 2026-08-25

Step 5 happened. `billycore peek` reached the live mailbox and these are counts
from it, not estimates. **Do not use Gmail's `resultSizeEstimate`** — it returned
201 for every query asked of it, including ones with three real hits. Every number
below comes from paginating message ids.

### Volume

| Query | Count |
|---|---|
| `from:nu@nu.com.mx`, all time | **1,044** |
| `from:marketing.nu.com.mx`, all time | 41 |
| `from:nu.com.mx newer_than:30d` | 54 |
| HSBC, all senders, all time | **36** |

### Nu templates

Four templates carry a financial event. They are 800 of 1,044 messages — **77%**,
and the top two alone are 67%.

| Subject | All time | Last 30d | Event |
|---|---|---|---|
| `Tu transferencia fue exitosa` | 354 | 15 | OUTFLOW |
| `¡Recibiste una transferencia!` | 345 | 22 | INFLOW |
| `¡Recibimos tu pago!` | 90 | 1 | Card payment |
| `Tu comprobante de pago de servicio` | 11 | 0 | Service payment |
| **transaction-bearing** | **800** | **38** | |
| contacts, card limits, statement notices, Apple Pay, marketing | 244 | 14 | none |

**The MVP month is 38 transactions.**

### Fields, by template

`Tu transferencia fue exitosa` — the richest:

```
Monto: $1,000.00            Folio: QURN4HRT7
Fecha: 16/AGO/2026          Nombre: <beneficiary>
Hora: 17:44                 Entidad: HSBC
Concepto: Transferencia     Tarjeta de débito: ••••7662
Número de referencia: 160826    Estatus: Completada
Clave de rastreo: NU3AG95KNAK990QOSMHUM86DL8SV
```

`¡Recibiste una transferencia!` — `Monto`, `Fecha: 18 AGO 2026`, `Hora`, sender
name in prose. No folio, no tracking key.

`¡Recibimos tu pago!` — bare amount line, product name, **no date in the body**.

`Tu comprobante de pago de servicio` — `Monto`, `Tipo de transacción`, merchant as
`Empresa:`, `Código de operación` (a UUID), `18 jul 2026 - 10:03:51`.

### Consequences for the design

1. **Three date formats across four templates**, with Spanish month names in two
   cases. One date parser will not serve; this argues for per-template parsers.
2. **`Clave de rastreo` is a natural key.** SPEI tracking keys are globally unique.
   Two artifacts sharing one are the same transaction with no ambiguity — a far
   stronger signal than the merchant/amount/time comparison in DOMAIN.md §6, and
   partial evidence toward DOMAIN.md Q14.
3. **`Tarjeta de débito: ••••7662` is the counterparty's account, not the user's.**
   It sits in the recipient block with `Nombre` and `Entidad`. Recording it as the
   user's AccountIdentifier would poison the account signal in DOMAIN.md §6, which
   treats a known-account contradiction as grounds to block reconciliation.
4. **`¡Recibimos tu pago!` carries no body date**, so `occurred_at` falls back to
   `internalDate`. This is exactly the rule DATA_MODEL.md §4.5 already specifies —
   now validated rather than assumed.
5. **Amounts are `$1,000.00`** — comma thousands separator, two decimals. Strip the
   comma, parse to minor units, never through a float.
6. **These emails carry tracking pixels.** SECURITY.md §7's "never resolve remote
   references" is not hypothetical: a parser that fetched images would report every
   re-parse to Nu's ESP.

### The card-spending gap

**Nu does not email card purchases.** Zero in 1,044 messages. They are push
notifications in the app. The `"compra aprobada"` hits in the mailbox are merchant
receipts from Mixup/iShop, not Nu.

The `estado de cuenta disponible` emails carry **no PDF** — checked, both are
`text/html` with no attachment. The statement is behind the app login.

**HSBC contributes nothing.** 36 emails all time: session summaries, security
alerts, payment reminders, promotions. No transaction notifications.

So Gmail as a Source means **Nu transfers and payments, and nothing else**. The
MVP table is an honest account-level view of money moving. It is not a record of
what the user bought.

> **Known mismatch with DOMAIN.md.** Its worked example is
> `Compra aprobada por $800 MXN en AMZN`. That email does not exist in this
> mailbox. Merchant normalization (DOMAIN.md Q2) and the AMZN/Amazon aliasing
> discussion solve a problem the real Evidence does not pose: transfer
> counterparties are person names and CLABE entities, not merchant descriptors.
> The domain question is settled first; then DOMAIN.md changes. Not decided here.

Recovering card spending is a **Source** problem, not a parser problem, and
PRODUCT.md already names bank statements as a first-class Source. Out of M1 scope.

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

Five open items that reading the documents together surfaced. **Four are now closed** —
D23, D24, D25, D26 — each by an author decision taken before the code that depended on
it, which is the protocol working rather than a formality observed. Item 4 was answered
by the `billycore auth` subcommand without a decision entry, because nothing was
genuinely in question once it was written.

1. ~~**`source_artifact_key` vs `source_reference` — three names for one concept.**~~
   **Closed by D23**: `source_reference` is canonical in code and schema, and
   `source_artifact_key` stays the wire spelling. The mapping now exists in exactly one
   file — `internal/adapter/api/api.go` — and nowhere else.
2. ~~**`content_type` is on the wire and not in the schema.**~~ **Closed by D24**:
   `content_type TEXT` is in migration 001 and carries `message/rfc822` for all 1,044
   rows; `content_bytes` stays derived from `length(raw_content)`. DATA_MODEL.md §4.1 is
   therefore one column behind the schema BillyCore creates — the author's correction to
   make, not a licence for the code to drift further.
3. ~~**Where does Source *configuration* live?**~~ **Closed by D26**:
   `~/.billy/sources.json`, beside the database and the credentials, holding no secret
   of its own. A missing file means no Source is configured and every sync answers
   `404`; a malformed one is a startup error, because a typo that silently produced an
   empty set would surface as that same `404`.
4. ~~**The OAuth grant is interactive; the daemon is not.**~~ **Settled in code**, with
   no decision entry: `billycore auth` performs the one interactive grant, `serve` never
   asks for a browser, and the access token is refreshed per sync rather than at startup
   — a client built once would stop working an hour into the daemon's life. No document
   describes the subcommand yet.
5. ~~**Is `EvidenceIngested` emitted in M1?**~~ **Closed by D25**: yes, and
   `domain_event` is in migration 001. The event is written in the same transaction as
   the row and only when a row was created, so a re-sync emits nothing. All 1,044
   Evidence rows have one. DOMAIN.md Q10 — which events v1 needs — stays open for the
   other five.

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
2. ~~**What gets cut?**~~ **Closed by D28: nothing.** The question was put directly on
   2026-08-25 with four options on the table — ship late, statement-only, reconcile by
   hand, or drop history — and the answer was to hold the scope and move the date. The
   risk this was guarding against has not gone away; it has changed shape, from *"what
   gets dropped at the last minute"* to *"does a movable deadline constrain anything"*.
3. **Repository layout — the root is fixed; one repo or three is not.**
   `billycore` is now its own git repository with its own remote
   (`github.com/alejandroayalad/billycore`) and its own `.gitignore`, which closes the
   dangerous half of this item: the repository root was the home directory, with
   `~/.ssh/`, `~/.env`, and `~/.claude.json` sitting untracked inside a live repo, and
   `billycore/.gitignore` protecting nothing above itself. Nothing leaked — there were
   no commits — and M1 wrote a real refresh token to disk only after the split.
   Still open: whether `billycore`, `billysat`, and `billyagent` are one repository or
   three for an open-source release. Cheap now, annoying later.

4. **Licence.** Undecided. Required before the repository goes public.
5. **Contribution model.** Open source with outside contributors implies issues, a
   README, and a CONTRIBUTING file. None exist. Not needed before 2026-09-22.

---

> Update this file when the state changes — not when the design changes. Design lives in
> `docs/`; decisions live in `ai/DECISIONS.md`; this file answers *"where are we?"*
