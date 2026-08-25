# BillyCore — API

The contract for BillyCore's public interface. A consumer should be able to build a
working client from this document alone.

This document **records** decisions; it does not make them.
[PRODUCT.md](PRODUCT.md) established that BillyCore is consumed through a public
interface and owns its own auth and versioning. [ARCHITECTURE.md](ARCHITECTURE.md) §6
chose HTTP + JSON at `/v1` and fixed the endpoint set. [DOMAIN.md](DOMAIN.md) defines
what the resources below actually mean. Where a domain question is still open, this
document describes the wire shape and says the question is open — it does not settle it
in passing.

---

## 1. Overview & conventions

Base URL is whatever address the binary is bound to. All financial resources live under
`/v1`.

### Versioning

`/v1` changes compatibly. A new field on a response, a new optional query parameter, or
a new enum member that existing values still cover is not a breaking change — clients
must ignore unknown fields. Removing a field, renaming one, changing a type, or
tightening validation on an accepted request is breaking, and requires `/v2`.

### JSON conventions

| Rule | Detail |
|---|---|
| Encoding | UTF-8. `Content-Type: application/json`. |
| Field names | `snake_case`. |
| Timestamps | RFC 3339, always UTC, always with offset: `2026-08-23T14:02:11Z`. |
| Identifiers | Opaque strings. Clients must not parse them. |
| Enums | Uppercase, exactly as spelled in DOMAIN.md. |
| Unknown fields | Ignored on request. Clients must ignore them on response. |

### Money

Money is exact and unsigned, per DOMAIN.md §3:

```json
{ "amount_minor": 80050, "currency": "MXN" }
```

`amount_minor` is an integer count of the currency's **minor unit**. It is never the
decimal amount, and the decimal amount never appears on the wire.

| Amount | On the wire |
|---|---|
| 899.93 MXN | `{ "amount_minor": 89993, "currency": "MXN" }` |
| 800.50 MXN | `{ "amount_minor": 80050, "currency": "MXN" }` |
| 12.00 USD | `{ "amount_minor": 1200, "currency": "USD" }` |
| ¥899 JPY | `{ "amount_minor": 899, "currency": "JPY" }` |

The conversion is `amount_minor = decimal × 10^exponent`, where the exponent depends on
the currency — 2 for MXN and USD, 0 for JPY, 3 for KWD. **`amount_minor` is meaningless
without its `currency`**, which is why Money is always transported as the pair and never
as a bare number.

A float, a decimal string, or a negative value is rejected — see §3. Direction is never
encoded in the amount; it is a separate `direction` field carrying `INFLOW` or
`OUTFLOW`.

> Clients must not compute this with floating-point arithmetic. `899.93 * 100` is
> `89992.999...` in IEEE 754 and truncates to `89992` — one centavo lost, silently, in
> the layer that exists specifically to prevent that.

### Absent values vs. weak values

DOMAIN.md §7 draws a distinction the wire format has to preserve: *Billy has no belief*
is not the same as *Billy has a weakly supported belief*.

| Situation | Representation |
|---|---|
| No Claim for this field | `null`, or the field is absent |
| A Claim with weak support | `{ "value": "****1234", "confidence": "LOW" }` |
| Financial status genuinely undetermined | `"financial_status": "UNKNOWN"` |

`UNKNOWN` appears on the wire only where it is a real domain value — `FinancialStatus`.
It is not a general-purpose placeholder for missing data.

### Pagination

Collection endpoints use cursor pagination. One mechanism, everywhere.

```
GET /v1/transactions?limit=50&cursor=eyJ0IjoiMjAyNi0wOC0yM...
```

| Parameter | Detail |
|---|---|
| `limit` | Optional. Default 50, maximum 200. |
| `cursor` | Optional. Opaque. Only ever a value returned by a previous response. |

```json
{
  "data": [ ... ],
  "next_cursor": "eyJ0IjoiMjAyNi0wOC0yM..."
}
```

`next_cursor` is `null` when the page is the last one. The cursor encodes an ordering
key — `(timestamp, id)` for transactions, `seq` for events — so ordering is
deterministic and a resource inserted mid-iteration cannot silently shift a page.

---

## 2. Authentication

A single bearer token, read from config at startup (ARCHITECTURE.md §6).

```
Authorization: Bearer <token>
```

Every `/v1` endpoint requires it. `/healthz` does not.

A missing, malformed, or incorrect token returns `401` with `type: "unauthorized"`. The
response never distinguishes *missing* from *wrong*.

The server binds to loopback by default. This is a single-user self-hosted service;
there are no scopes, no per-consumer tokens, and no token rotation endpoint.

---

## 3. Error model

One envelope for every failure:

```json
{
  "error": {
    "type": "invariant_violation",
    "message": "The proposed Claim was understood but violates domain invariants.",
    "violations": [
      {
        "code": "money.negative_not_allowed",
        "field": "fields.amount.value",
        "detail": "amount_minor must be a non-negative integer; direction carries sign"
      }
    ]
  }
}
```

`violations` is present on `400` and `422`, and absent otherwise.

### Status codes

| Status | `type` | Meaning |
|---|---|---|
| `400` | `malformed_request` | The request could not be understood. Bad JSON, wrong type, unparseable parameter. |
| `401` | `unauthorized` | Missing or invalid bearer token. |
| `404` | `not_found` | The resource does not exist. |
| `409` | `conflict` | The request is valid but conflicts with current state. |
| `422` | `invariant_violation` | The request was understood and BillyCore refuses the financial interpretation. |
| `500` | `internal_error` | A bug. |
| `503` | `unavailable` | The service cannot serve the request right now. |

### Why `400` and `422` are different

This distinction is the reason the error model is defined before the endpoints. A
consumer — BillyAgent above all — has to be able to tell *"your JSON was bad"* from
*"your proposal was understood and BillyCore refuses it."*

The first is a client bug: fix the serialization and retry. The second is a substantive
answer about the financial domain, and retrying the same body will always fail. Only the
second is worth surfacing to a user or feeding back into an extraction strategy.

### Violation codes

Codes are stable, namespaced, and machine-readable. `field` is a JSON path into the
request body when the violation is attributable to one field, and `null` when it is a
property of the request as a whole.

| Code | Raised when |
|---|---|
| `money.not_integer` | An amount was submitted as a float or a string. |
| `money.negative_not_allowed` | An amount was signed. Direction carries sign. |
| `currency.invalid` | The currency is not a recognized code. |
| `provenance.required` | A Claim was proposed without reference to Evidence. |
| `provenance.evidence_not_found` | The referenced Evidence does not exist. |
| `claim.unknown_field` | A proposed field is not part of the Claim field set. |
| `claim.confidence_invalid` | Confidence is not `HIGH`, `MEDIUM`, or `LOW`. |
| `reconciliation.contradiction` | The proposal merges observations that contradict. `detail` names the field. |
| `reconciliation.unknown_transaction` | A referenced Transaction does not exist. |

The set is extensible: a new code may be added within `/v1`, and clients must treat an
unrecognized code as a generic rejection rather than failing. Which contradictions are
strong enough to reject at all is DOMAIN.md open questions 7 and 8, still open.

---

## 4. Resource representations

Defined once here. The endpoint sections reference these rather than re-inlining them.

### Evidence

Immutable once recorded (DOMAIN.md §5). Evidence has no domain state, and the pipeline
stage it happens to be in is not exposed.

```json
{
  "id": "ev_01J8X2",
  "source_id": "gmail_primary",
  "source_artifact_key": "18f2a9c4d1e77b03",
  "observed_at": "2026-08-23T14:02:11Z",
  "content_type": "text/html",
  "content_bytes": 8241
}
```

`raw_content` is not included in list or nested representations. It is returned only by
`GET /v1/evidence/{id}` — see §6.

### Claim

An interpretation of Evidence, with field-level confidence (DOMAIN.md §7).

```json
{
  "id": "cl_01J8X4",
  "evidence_id": "ev_01J8X2",
  "state": "ACTIVE",
  "superseded_by": null,
  "created_at": "2026-08-23T14:02:19Z",
  "fields": {
    "amount":           { "value": 80050,      "confidence": "HIGH" },
    "currency":         { "value": "MXN",      "confidence": "HIGH" },
    "merchant":         { "value": "Amazon",   "confidence": "MEDIUM" },
    "account":          { "value": "****1234", "confidence": "LOW" },
    "direction":        { "value": "OUTFLOW",  "confidence": "HIGH" },
    "occurred_at":      { "value": "2026-08-23T13:58:00Z", "confidence": "MEDIUM" },
    "financial_status": { "value": "PENDING",  "confidence": "MEDIUM" }
  }
}
```

A field with no Claim is absent from `fields` entirely. It is never present with a
`null` value, and never present as `"UNKNOWN"`.

`state` is `PROPOSED`, `ACTIVE`, `SUPERSEDED`, or `REJECTED` (DOMAIN.md §5). `ACTIVE`
means Billy currently uses this Claim — not that it is objectively true.

`amount` and `currency` are separate fields carrying independent confidence, matching
DOMAIN.md §7's own example. They are assembled into a Money value on Transaction.

### Transaction

```json
{
  "id": "tx_01J8X7",
  "amount": { "amount_minor": 80050, "currency": "MXN" },
  "direction": "OUTFLOW",
  "financial_status": "PENDING",
  "reconciliation_state": "UNRECONCILED",
  "occurred_at": "2026-08-23T13:58:00Z",
  "merchant": { "value": "Amazon", "confidence": "MEDIUM" },
  "account": null,
  "evidence_ids": ["ev_01J8X2"],
  "relationships": [
    { "kind": "REFUND_OF", "transaction_id": "tx_01J8Q1" }
  ]
}
```

`reconciliation_state` and `financial_status` are separate concerns and never collapse
into one field (DOMAIN.md §5). There is no `confirmed` flag: provenance, active Claims,
reconciliation state, financial status, and confidence together are more expressive.

The vocabulary of `relationships` is DOMAIN.md open question 9 — the shape is fixed,
the `kind` set is not.

### AmbiguousReconciliation

**A representation, not an entity.** The v1 entity list is Evidence, Claim, and
Transaction (DOMAIN.md §3); this is how BillyCore renders reconciliation uncertainty
over the wire, and it introduces no fourth aggregate root.

```json
{
  "id": "rc_01J8XB",
  "left_transaction_id": "tx_01J8X7",
  "right_transaction_id": "tx_01J8XA",
  "outcome": "AMBIGUOUS",
  "comparisons": {
    "merchant":         "MATCH",
    "amount":           "MATCH",
    "currency":         "MATCH",
    "direction":        "MATCH",
    "account":          "MISSING",
    "occurred_at":      "MATCH",
    "financial_status": "CONTRADICTION"
  },
  "evaluated_at": "2026-08-23T14:03:02Z"
}
```

Each comparison is `MATCH`, `MISSING`, or `CONTRADICTION` (DOMAIN.md §6). `MISSING`
weakens the candidate without contradicting it.

### DomainEvent

```json
{
  "seq": 4187,
  "type": "TransactionReconciled",
  "occurred_at": "2026-08-23T14:03:02Z",
  "payload": { }
}
```

`seq` is monotonic and gap-free. Only meaningful business facts are events (DOMAIN.md
§8) — pipeline progress is not among them.

---

## 5. Sources

### `POST /v1/sources/{id}/sync`

Fetch from a configured Source and record whatever Evidence it yields.

Returns `200` with a summary once the fetch has been recorded:

```json
{
  "source_id": "gmail_primary",
  "artifacts_discovered": 34,
  "evidence_created": 6,
  "evidence_skipped": 28,
  "completed_at": "2026-08-23T14:02:14Z"
}
```

`evidence_skipped` counts artifacts whose `source_artifact_key` already existed — the
same email fetched twice produces one Evidence row (ARCHITECTURE.md §5).

> **This endpoint is synchronous, and returns `200`, not `202`.**
>
> Staged processing does not imply an asynchronous HTTP contract. Sync records
> Evidence; extraction and reconciliation advance behind it on their own passes, and the
> caller does not wait for them. There is no job resource and nothing to poll.
>
> Domain events are explicitly *not* the mechanism for tracking sync progress. Pipeline
> stages are application concerns (DOMAIN.md §5) and domain events carry business facts
> (§8); using one to observe the other conflates them.
>
> If real syncs become slow enough that a synchronous response is untenable, the answer
> is a `SyncRun` application resource and `202 Accepted` — introduced then, not now.

`404` if the Source is not configured. `409` if a sync for that Source is already in
flight.

---

## 6. Evidence

### `POST /v1/evidence`

Record a source artifact directly.

```json
{
  "source_id": "manual",
  "source_artifact_key": "statement-2026-07.pdf",
  "observed_at": "2026-08-23T14:02:11Z",
  "content_type": "application/pdf",
  "raw_content": "<base64>"
}
```

Ingestion is an upsert on `(source_id, source_artifact_key)`. Submitting a key that
already exists returns `200` with the existing Evidence and does not modify it —
Evidence is immutable. A first recording returns `201`.

This makes the endpoint naturally idempotent: the artifact key *is* the idempotency key,
and a retried request is safe.

Whether this endpoint should be public at all, or whether direct submission is an
internal concern of the Source adapters, is ARCHITECTURE.md open question 3.

### `GET /v1/evidence/{id}`

Returns the Evidence representation with `raw_content` included, base64-encoded.

This is the only endpoint that returns raw content. Nothing queries inside it, and it
can be large.

---

## 7. Transactions

### `GET /v1/transactions`

The endpoint the MVP success criterion is measured against — *"my last month of
transactions, well classified, in a good table."*

| Parameter | Values | Detail |
|---|---|---|
| `from` | RFC 3339 | Inclusive lower bound on `occurred_at`. |
| `to` | RFC 3339 | Exclusive upper bound on `occurred_at`. |
| `direction` | `INFLOW` · `OUTFLOW` | |
| `financial_status` | `UNKNOWN` · `PENDING` · `SETTLED` · `REVERSED` | Repeatable. |
| `reconciliation_state` | `UNRECONCILED` · `RECONCILED` · `CONFLICTED` | Repeatable. |
| `currency` | e.g. `MXN` | |
| `limit`, `cursor` | | See §1. |

Repeatable parameters OR together; distinct parameters AND together.

Ordered by `occurred_at` descending, `id` descending as a tiebreak. A Transaction whose
`occurred_at` is unknown sorts by its earliest Evidence `observed_at`; the response is
unaffected.

Returns a paginated list of Transaction representations.

### `GET /v1/transactions/{id}`

One Transaction with its full provenance trail:

```json
{
  "transaction": { },
  "claims": [ ],
  "evidence": [ ]
}
```

`claims` includes `SUPERSEDED` and `REJECTED` Claims, not only `ACTIVE` ones — the
audit trail is the point. `evidence` carries the Evidence representations without
`raw_content`.

---

## 8. Claim proposals

### `POST /v1/claims`

Propose an interpretation of existing Evidence.

```json
{
  "evidence_id": "ev_01J8X2",
  "supersedes": null,
  "fields": {
    "amount":    { "value": 80050,     "confidence": "HIGH" },
    "currency":  { "value": "MXN",     "confidence": "HIGH" },
    "merchant":  { "value": "Amazon",  "confidence": "MEDIUM" },
    "direction": { "value": "OUTFLOW", "confidence": "HIGH" }
  }
}
```

> **A proposal is not an instruction.**
>
> The body carries an interpretation and its provenance. BillyCore validates it through
> the same domain constructor an internal parser goes through (ARCHITECTURE.md §4), and
> answers. There is exactly one place where *"is this a valid Claim?"* is decided, and
> it is not the HTTP layer.

`201` with the accepted Claim, or `422` with the violations that rejected it.

A proposer cannot create a Claim without provenance to existing Evidence, submit Money
as a float or a signed value, or assert a confidence outside `HIGH` / `MEDIUM` / `LOW`.
Acceptance does not mean the interpretation is true — it means the interpretation is
well-formed and supported by Evidence.

`supersedes` names a Claim this one replaces. The superseded Claim moves to
`SUPERSEDED` and is retained for provenance. Whether a Claim may draw on more than one
piece of Evidence is DOMAIN.md open question 13; until it is answered, `evidence_id` is
singular and required.

This endpoint ships in v1, before BillyAgent exists. It is the escape hatch that makes
"well classified" reachable when the deterministic parsers fall short
(ARCHITECTURE.md §8).

---

## 9. Reconciliation

### `POST /v1/reconciliations`

Propose that two observations describe the same Transaction.

```json
{
  "left_transaction_id": "tx_01J8X7",
  "right_transaction_id": "tx_01J8XA",
  "proposed_outcome": "MATCH",
  "rationale": "Same merchant and amount; settlement of the earlier authorization."
}
```

> **A proposal never directly mutates reconciliation state.**
>
> BillyCore evaluates the proposal against its own comparison of the two observations
> and either accepts or rejects it. `proposed_outcome` is an argument, not a command —
> BillyAgent cannot bypass BillyCore merely because a model believes two observations
> match (DOMAIN.md §6).

Accepted:

```json
{
  "outcome": "MATCH",
  "surviving_transaction_id": "tx_01J8X7",
  "comparisons": { }
}
```

Rejected: `422`, with `comparisons` in `detail` showing which fields contradicted.

**What is deliberately not decided here.** Whether an agent-proposed `MATCH` may resolve
a case BillyCore deterministically evaluates as `AMBIGUOUS` is a domain question, and it
is open — DOMAIN.md §6 lists an agent proposal among the things that can resolve
ambiguity, while open questions 11 and 12 leave the representation and the
deterministic-vs-proposed distinction unsettled. This document fixes the wire shape and
the rule that Core decides. It does not answer that question, and an implementation
should not answer it by accident.

`surviving_transaction_id` is likewise provisional: DOMAIN.md open question 15 has not
settled whether a `MATCH` means one Transaction absorbs the other or a new Transaction
supersedes both. The field names the survivor either way.

### `GET /v1/reconciliations/ambiguous`

The `AMBIGUOUS` queue from DOMAIN.md §6 — observations BillyCore could not safely
decide, preserved separately rather than merged.

Paginated list of AmbiguousReconciliation representations, ordered by `evaluated_at`
descending.

This is what a human or an agent reads before proposing a resolution through
`POST /v1/reconciliations`.

---

## 10. Domain events

### `GET /v1/events`

Meaningful business facts, in order.

| Parameter | Detail |
|---|---|
| `type` | Optional, repeatable. Filter by event type. |
| `limit`, `cursor` | See §1. The cursor encodes `seq`. |

Event types are those in DOMAIN.md §8: `EvidenceIngested`, `ClaimActivated`,
`TransactionCreated`, `TransactionReconciled`, `ReconciliationAmbiguous`,
`TransactionFinancialStatusChanged`. Which of these v1 actually needs is DOMAIN.md open
question 10; clients must ignore types they do not recognize.

`seq` is monotonic and gap-free, so a consumer that stores the last `seq` it processed
resumes exactly where it stopped. The cursor is client-supplied state — BillyCore keeps
no per-consumer position (ARCHITECTURE.md open question 4).

There is no bus, no subscription, and no webhook. Nothing subscribes yet
(ARCHITECTURE.md §9).

> This endpoint reports **what happened to the financial domain**. It is not a progress
> feed for the ingestion pipeline, and it must not be used as one.

---

## 11. Operational health

### `GET /healthz`

```json
{ "status": "ok", "api_version": "v1" }
```

Unauthenticated. Deliberately outside `/v1`: it is an operational endpoint, not part of
the versioned financial resource model, and it should never acquire a dependency on
one.

Returns `200` when the process can serve requests and reach its database, `503`
otherwise. Nothing else belongs here — no queue depths, no counts, no build metadata
until something needs them.

---

## 12. Open questions

1. Does `POST /v1/claims` need an explicit idempotency key? `POST /v1/evidence` gets
   idempotency free from `source_artifact_key`; a retried Claim proposal currently
   creates a second Claim.
2. `amount` and `currency` are separate Claim fields with independent confidence, but
   assemble into one Money value on Transaction. Is independent confidence on
   `currency` ever meaningful, or is it always the confidence of `amount`?
3. Should `GET /v1/transactions` expose a filter for confidence — "show me everything
   Billy is unsure about"? It is the natural triage query and nothing supports it yet.
4. Is `rationale` on a reconciliation proposal stored, and if so is it provenance or
   diagnostics? DOMAIN.md open question 11 (auditing human and agent resolutions) will
   likely decide this.
5. What does `GET /v1/transactions/{id}` return for a Transaction whose Evidence the
   user deleted? DOMAIN.md open question 19 and ARCHITECTURE.md open question 5.
6. Does the ambiguous queue need an explicit dismissal — "these two are genuinely
   different, stop asking" — or is that a `NO_MATCH` proposal through
   `POST /v1/reconciliations`?
7. Should `raw_content` be a sub-resource (`GET /v1/evidence/{id}/content`) rather than
   an inline base64 field? A large PDF inside a JSON body is awkward for both sides.
8. **Where does the minor-unit exponent per currency come from?** `amount_minor` cannot
   be rendered or validated without it, and nothing has decided whether BillyCore ships
   a currency table, hardcodes the currencies it supports, or leaves the exponent to the
   consumer. Related to DOMAIN.md open question 4.
9. Should `POST /v1/claims` accept a decimal **string** — `"899.93"` — and convert it
   server-side? It would remove the most likely client mistake, but it makes Core
   responsible for the exponent table in question 8, and it puts a second amount format
   in a contract whose whole point is having one.
