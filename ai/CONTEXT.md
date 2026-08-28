# BillyCore — Context

**Last updated:** 2026-08-28

Orientation for anyone — human or agent — starting work on BillyCore. `docs/` describes
the system as designed. This file describes where the project actually *is*, which is a
different and much shorter story.

Read this first, then [docs/PRODUCT.md](../docs/PRODUCT.md).

---

## 1. State: the email half of M2 is done — real Transactions, from the real mailbox

The pipeline runs end to end. Email goes in at one end and financial facts come out
at the other.

On **2026-08-28** `billycore serve` drained the whole stored corpus in about three
seconds:

| | |
|---|---|
| Evidence | 1,044 — every row at `RECONCILED` |
| Interpretations | 800 |
| Claims | 800, all `ACTIVE` |
| Transactions | 800, all `ACTIVE` |
| Unrecognised | 244 |
| **Failed** | **0** |

A second start wrote nothing: no row was claimable, and the counts were identical
afterwards. Extraction and reconciliation are idempotent the way ingestion is — because
a constraint says so, not because a code path remembered to check.

**Every number the design predicted, it predicted correctly.** These were written down
before the code ran, which is the only thing that makes the agreement worth anything:

| Predicted | Where | Measured |
|---|---|---|
| 800 transaction-bearing, 244 not | §3.1 below | 800 / 244 |
| 345 inflows | §3.1 | 345 |
| 455 outflows — 354 + 90 + 11 | §3.1 | 455 |
| 101 Transactions at `UNKNOWN` | D43 | 101 |
| 90 dated from the `observed_at` fallback | DATA_MODEL.md §4.5, D33 | 90 |
| 16 artifacts carrying a `tracking_key` | D42 | 16 |

The derived data is sane: one currency (MXN, D30); no null, zero or negative amount;
no date in the future; `occurred_at` spanning 2024-01-24 to 2026-08-19. The 90
Transactions with no merchant are exactly the card payments, whose template carries no
counterparty — which is also why they are the 90 taking the date fallback. Recognition
holds at 76–77% across 2024, 2025 and 2026, so no template has drifted by era.

**Last 30 days: 37 Transactions, 21 in, 16 out.** §3.1 predicted 37, measured on
2026-08-25 against Gmail's received date; this counts `occurred_at` on 2026-08-28. They
are different windows over different clocks, so the totals agreeing is closer to
coincidence than to confirmation. The number to trust is the shape, not the match.

What runs today: `billycore auth`, `billycore peek`, and `billycore serve` — `GET
/healthz`, `POST /v1/sources/{id}/sync`, `GET /v1/evidence/{id}`, the last two behind a
bearer token compared in constant time — plus one background pipeline worker draining
extraction and then Transaction construction (D45).

What does not exist: **no way to see any of this that is not SQL**, no bank statements,
no reconciliation between Sources, no AI, and no HSBC. The success criterion in §2 is a
*table*, and 800 rows nobody can look at do not satisfy it. That is now the whole gap
for the email half, and §8 item 1 is the critical path.

Two things the code can do that nothing has yet asked it to do: re-extraction (D48) has
never run against the live database — `superseded_by_interpretation_id` is NULL on all
800 rows — and no trigger exposes it. `superseded_by_transaction_id` exists and is never
written (D49, open).

### Code

| Path | Code | Test | State |
|---|---|---|---|
| `cmd/billycore/` | 597 | 387 | `serve`, `auth`, `peek`; the background pipeline worker (D45) |
| `internal/domain/` | 1,229 | 1,026 | `Money`, `Evidence`, `Claim`, `Interpretation`, `Transaction`, the D6 assertion |
| `internal/app/` | 1,061 | 1,851 | Ingest, extract, reconcile; the ports each consumes |
| `internal/adapter/store/sqlite/` | 1,338 | 2,391 | Five migrations, four repositories, the pipeline queue |
| `internal/adapter/parser/` | 1,686 | 1,663 | MIME, HTML, money, dates; the four Nu templates |
| `internal/adapter/source/gmail/` | 680 | 234 | OAuth, `ListIDs`, `GetMetadata`, `GetRaw`, `Fetcher` |
| `internal/adapter/api/` | 402 | 509 | Error envelope, bearer auth, the `/v1` routes |
| `internal/adapter/config/` | 92 | 93 | `sources.json` (D26) |
| `internal/id/` | 38 | 32 | UUID v4; outside the domain because randomness is I/O |
| **Total** | **7,123** | **8,186** | `make check` green, and green under `-race` |

More test than code, for the first time. Schema: `001_initial`, `002_claims`,
`003_active_claim`, `004_transactions`, `005_interpretations` — the live database is at
`user_version = 5`.

### Documents

| Document | State |
|---|---|
| `ai/DECISIONS.md` | Written · D1–D49 |
| `AGENTS.md` | Written · §8 now carries the ASD-STE100 comment rule |
| `ai/CONTEXT.md` | This file |
| `docs/PRODUCT.md` | Written |
| `docs/ARCHITECTURE.md` | Written · §5's stages now exist in code |
| `docs/DOMAIN.md` | Written · **behind**: no Interpretation, and §8 does not list the two new `ClaimActivated` fields |
| `docs/DATA_MODEL.md` | Written · **behind**: no `interpretations`, `interpretation_claims`, `evidence_active_interpretation`; no `transaction_state` |
| `docs/API.md` | Written · §5 and §6 have running implementations |
| `docs/SECURITY.md` | Written |
| `ai/CONVENTIONS.md`, `ai/CONSTRAINTS.md`, `ai/WORKFLOW.md`, `ai/GLOSSARY.md` | **Empty** |
| BillySat, BillyAgent | Docs-only scaffolds, no code. Do not start them. |

`docs/` is where the drift is, and it is drift of exactly one kind: the schema and the
domain grew a concept — the Interpretation — that the documents describing them have
never heard of. Correcting them is the author's (AGENTS.md §5).

---

## 2. The clock

| | |
|---|---|
| Started | 2026-08-22 |
| MVP target | 2026-09-22 — a target, no longer hard (D28) |
| Budget | 2 h/weekday + 4 h Saturday + 4 h Sunday ≈ **18 h/week** |
| Remaining to the target | ≈ **64 hours** |
| Estimated to finish the scope | ≈ **50–60 hours** |

**Those two lines agree for the first time.** They have not agreed since D27 put bank
statements and reconciliation into the MVP, and the reason they agree now is that the
largest single item is done: M2's email half was scoped at ~45 h and it has landed and
run against real data.

Do not read that as slack. What remains is statements (~25 h), reconciliation (~20 h),
and the viewing surface D32 specified but nobody has estimated. Statement parsing is
still the piece most likely to blow its estimate, and it is still the only one whose
input format has never been seen — the same sentence this file has carried since M2 was
scoped, now with fewer hours behind it.

The success criterion has not moved (PRODUCT.md):

> I can see my last month of transactions, well classified, in a good table with good
> financial information.

**Nothing that does not serve that sentence gets built.** Note which word is now the
binding one: *see*. Billy holds 37 well-classified Transactions for the last 30 days and
can show them to nobody.

---

## 3. Next step

**Make the 800 Transactions visible.** D32 already decided the shape — a terminal table
and a plain web page — and neither exists.

That is the whole recommendation, and it is a change from the order this file carried
through M2. Statements and reconciliation are larger and more interesting; the viewing
surface is the one that converts work already done into the success criterion. It is
also the fastest way to find out whether the 800 rows are *right*, because a table a
human reads is a better parser test than any assertion in `nu_test.go`.

The slices, in order:

1. ~~`go mod init`, one binary that serves `/healthz`~~ — **done**
2. ~~SQLite, embedded migrations, `evidence`~~ — **done**
3. ~~Gmail OAuth, read-only, token at `0600`~~ — **done**
4. ~~Fetch → Evidence at `RECEIVED`~~ — **done**, 1,044 rows
5. ~~Look at real Nu and HSBC email~~ — **done**, §3.1 is what it said
6. ~~Per-template parsers, Claims with field-level confidence, `EXTRACTED`~~ — **done**
7. ~~Transactions from active Claims; the background worker~~ — **done**, 800 rows
8. **`GET /v1/transactions`, a `billycore tx` table, and one static page (D32)**
9. Statements: intake by hand through `POST /v1/evidence`, then PDF text extraction
10. Reconciliation: DOMAIN.md §6's six signals, and transfers that appear in both Sources

**The order still matters.** 9 before 10, because reconciliation cannot be tested until
there are two kinds of Transaction to reconcile. 8 first because it is cheap, it is on
the critical path for the criterion, and it audits everything 6 and 7 produced.

Two smaller things that are logged and unbuilt, either of which can be picked up in an
hour when it becomes annoying: the re-extraction trigger (D48 — the mechanism is written
and tested, nothing calls it) and a writer for `POST /v1/claims`, which D46 took away
when the unit of activation became a set.

---

## 3.1 What the real data says — measured 2026-08-25

Step 5 happened. `billycore peek` reached the live mailbox and these are counts
from it, not estimates. **Do not use Gmail's `resultSizeEstimate`** — it returned
201 for every query asked of it, including ones with three real hits. Every number
below comes from paginating message ids.

> **Confirmed by the pipeline on 2026-08-28.** Every count in this section survived
> contact with the parsers: 800 transaction-bearing, 244 not, 345 inflows, 455 outflows.
> The section was written from message ids and subject lines; the run read the bodies.
> They agree, which is the strongest evidence this file holds that the design was
> reasoning about the real mailbox rather than about itself.

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

Three were parked here as "not yet". Two of them are now next:

- **API.md Q8 — the currency minor-unit exponent. This blocks §3 step 8.** It was filed
  as blocking *rendering* amounts rather than storing them, which was correct and is no
  longer a reason to defer it: rendering amounts is precisely what the terminal table
  and the web page do. 800 rows hold `amount_minor` and `MXN`, and nothing yet knows
  that MXN divides by 100. Answer it before the table, not during it.
- **DATA_MODEL.md Q1 — what a reconciliation candidate references.** Blocks
  reconciliation, now the last slice rather than "week three at the earliest". D42 made
  it answerable and deliberately did not answer it: a candidate referencing Transaction
  ids is the reading the code supports.
- Everything in DOMAIN.md §10 — still not urgent, with one exception now visible from
  the code: Q13, whether a Claim may draw on several artifacts, is the question standing
  between one-for-one supersession and D49's recompute rule.

---

## 5. Open questions in this repo

There are around thirty across the six documents, and they are deliberate — the docs
refuse to invent answers that no requirement has forced yet.

**Protocol when work hits one:** stop, ask, and log the answer in `ai/DECISIONS.md`.
Do not pick silently, and do not code around it. A decision made in passing inside an
implementation is exactly what these documents exist to prevent.

**M2 is the evidence that this works.** Twenty-one entries — D29 through D49 — were
logged while the email half was built, each before the code that depended on it. Two of
them are the protocol catching something expensive: D46, where statement ingestion ran
into a cardinality D38 had settled three days earlier and the schema had to change
before a line of statement code was written; and D47, where the question *"has
`superseded_by_claim_id` ever been used?"* turned a speculative lineage design into a
one-paragraph answer. Neither would have surfaced from writing the code first.

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

1. ~~**How is the result actually seen?**~~ **Decided by D32** — a `billycore tx`
   terminal table and a plain web page fed by `GET /v1/transactions` — and **built by
   nothing.** This has moved from the hardest open question in this section to the
   shortest path on the board: the decision is made, the data exists, and 800
   Transactions are sitting in SQLite where only SQL can reach them. It is §3 step 8 and
   it is the critical path. The MVP is not demonstrable without it, and everything else
   in M2 now depends on it less than it depends on being *seen*.
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
