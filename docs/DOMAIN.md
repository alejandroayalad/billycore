# BillyCore — Domain Model

BillyCore transforms financial evidence into a trustworthy normalized representation
while preserving provenance and uncertainty.

It is not trying to model the entire banking system. Concepts in this document exist
because the domain needs them. Anything undecided lives in [Open Questions](#10-open-questions).

---

## 1. Ubiquitous Language

### Source

The external origin from which Billy receives information — an email provider, a bank
integration, a SAT integration in the future.

A Source is not financial truth. It produces source artifacts that Billy preserves as
Evidence.

### Evidence

Something Billy actually observed from a Source: an original bank email, a
notification, a transaction artifact from an API.

Evidence preserves what was actually observed, and is **immutable once recorded**.

### Observation

Not used. *Observation* would mean the same thing as Evidence, and two words for one
concept adds entropy without adding meaning.

**Evidence is the canonical term.** Where this document says "two observations", read
"two pieces of Evidence, or the Claims derived from them".

### Claim

An interpretation extracted from Evidence. **A Claim is not financial fact.**

> **Evidence:** `Compra aprobada por $800 MXN en AMZN`
>
> **Claim:**
> - merchant = Amazon
> - amount = 800 MXN
> - direction = OUTFLOW
> - financialStatus = PENDING

Claims may be incomplete, uncertain, superseded, or rejected.

### Transaction

The financial event Billy currently believes exists, based on one or more pieces of
Evidence and their Claims.

A Transaction has identity and a lifecycle. Multiple pieces of Evidence may refer to
the same Transaction.

### Account

Billy does not model a full Account entity in v1.

Where Evidence identifies an account or card, Billy records an **AccountIdentifier**
(for example, `card ending 1234`). A real Account entity may arrive later, when Billy
can reliably identify and track accounts across Sources.

### Reconciliation

The process of determining whether two Claims or pieces of Evidence refer to the same
real-world Transaction.

### Confidence

How strongly Billy supports a Claim or interpretation. Confidence represents
**uncertainty, not truth**.

---

## 2. Core Domain Insight — Evidence vs. Truth

This is the foundational rule of BillyCore.

> The original source artifact is Evidence.
> Anything extracted or inferred from it is a Claim, not financial truth.

Everything else in this document follows from it.

Billy may be incomplete, uncertain, or temporarily wrong. What Billy may never do is
assert a financial fact that nothing observed supports.

> **Billy may be uncertain, incomplete, or wrong. It may never be unsupported.**

Two consequences:

- Every Transaction remains traceable back to its Evidence.
- When an interpretation changes, the **Claim** is updated or superseded. The Evidence
  is never rewritten to match the new interpretation.

---

## 3. Entities & Value Objects

### Entities

The v1 entity list: **Evidence**, **Interpretation**, **Claim**, **Transaction**.

#### Evidence

Has identity because a particular source artifact matters independently of another
artifact that happens to look identical.

Conceptually carries:

- `id`
- source reference
- the original raw content, or an immutable reference to it
- observed / received timestamp

#### Interpretation

Has identity because one artifact can be read more than once, and each reading is a
whole that supersedes the last, not a Claim edited in place (D46). One Evidence has at
most one *active* Interpretation, which owns a set of Claims — one bank statement is one
Interpretation of about 180 movements. Re-reading the same Evidence under a better
profile produces a new Interpretation that supersedes the old one (D48), so the Claims
of a reading move together and never interleave two readings.

Conceptually carries:

- `id`
- the `Evidence` it reads
- the extraction profile that read it
- how many rows the reading could not parse (its unread count)
- the Interpretation it supersedes, if any

#### Claim

Has identity because two Claims may hold the same values and still differ: they may
originate from different Evidence, carry different confidence, be accepted or rejected
independently, or supersede one another.

#### Transaction

Has identity because Billy tracks the same financial event over time as more Evidence
arrives.

> A pending authorization and a later settlement may both refer to the same
> Transaction.

**Not** modeled as separate entities: Purchase, Refund, Income, Transfer. These are
represented as Transactions plus relationships between Transactions, until a
requirement proves otherwise.

### Value Objects

#### Money

Money must be exact.

- `amountMinor`: integer
- `currency`

`800.50 MXN` is stored as `amountMinor = 80050`. **Never floating point.**

Money does not encode direction. Billy uses:

```
Money(800 MXN) + Direction(OUTFLOW)
```

not:

```
Money(-800 MXN)
```

so the same information is never represented twice.

#### Currency

For example `MXN`, `USD`.

#### TransactionDirection

`INFLOW` · `OUTFLOW`

#### FinancialStatus

`UNKNOWN` · `PENDING` · `SETTLED` · `REVERSED`

No larger banking-specific status vocabulary yet.

#### AccountIdentifier

A value identifying the account or card referenced by Evidence.

#### DateRange

Immutable: `from`, `to`.

#### SourceReference

A stable reference back to the original Source artifact.

#### Confidence

`HIGH` · `MEDIUM` · `LOW`

Qualitative on purpose. Billy does not expose fake precision such as `0.87342` as
domain truth. BillyAgent may work with numerical model scores internally; BillyCore's
domain does not depend on AI-provider-specific confidence semantics.

---

## 4. Aggregates & Invariants

Evidence, Claim, and Transaction are **independent aggregate roots**. There is no
single giant aggregate, and none should be introduced without a real consistency
requirement.

### Evidence aggregate

> **Invariant: recorded Evidence is immutable.**

If Billy's interpretation was wrong, the Claim changes — the Evidence does not.

### Claim aggregate

> **Invariant: every Claim traces back to the Evidence it was derived from.**
> A Claim without provenance is invalid.

Claims evolve by replacement and supersession. Historical Claims are not deleted.

### Transaction aggregate

> **Invariant: every Transaction traces back to at least one piece of Evidence.**
> A Transaction without provenance is invalid.

Further invariants:

- Money uses integer minor units.
- Currency is required wherever Money exists.
- Direction is explicit.
- Money is unsigned / absolute.
- Unsupported financial facts are forbidden.

Evidence content is not duplicated inside a Transaction when a reference is enough.

### Refunds do not need a Purchase aggregate

```
Transaction A:  800 MXN  OUTFLOW
Transaction B:  800 MXN  INFLOW
Relationship:   B refunds A
```

A full refund produces a net economic effect of zero without deleting or rewriting
the original Transaction. A partial refund works the same way.

---

## 5. Lifecycle / State Model

**The ingestion pipeline has stages. Domain objects have states. These are not the
same thing.**

There is no single `INGESTED → PARSED → CLAIMED → RECONCILED → CONFIRMED` machine on
Transaction. A Transaction is never "parsed" — Evidence is parsed, by application and
infrastructure logic.

### Evidence

Immutable once recorded. Evidence has no domain state.

Processing statuses such as `PARSING`, `LLM_FAILED`, or `RETRYING` belong outside the
core domain.

### Interpretation lifecycle

An Interpretation is either the active reading of its Evidence or superseded by a later
one. It carries no `PROPOSED` or `REJECTED` state: a reading is activated as a whole
when it is stored, and replaced as a whole when a better one arrives (D46, D48). One
Evidence has at most one active Interpretation at a time, and the supersession keeps the
old reading for provenance.

### Claim lifecycle

| State | Meaning |
|---|---|
| `PROPOSED` | A Claim of an Interpretation, not yet part of Billy's active reading. |
| `ACTIVE` | Billy currently uses this Claim. **Active does not mean objectively true.** |
| `SUPERSEDED` | A newer or better Claim replaced it. Kept for provenance and audit. |
| `REJECTED` | The interpretation was determined not to be usable. |

A Claim is activated as part of an Interpretation, not on its own: the unit of
activation is the set, so a whole reading becomes active or is superseded together
(D46).

### Transaction lifecycle state

Kept separate from both reconciliation and financial status, because it answers a third
question — is this Transaction still one Billy shows?

`ACTIVE` · `SUPERSEDED`

A Transaction is `ACTIVE` until reconciliation finds it is the same event as another and
merges the two: the survivor stays `ACTIVE`, and the other becomes `SUPERSEDED` and
points at the survivor (D70). A `SUPERSEDED` Transaction leaves every list and total but
stays in history, so the merge is auditable and the two-source fact is not lost. Nothing
outside reconciliation sets this state.

### Transaction reconciliation state

Kept separate from financial status:

`UNRECONCILED` · `RECONCILED` · `CONFLICTED`

### Transaction financial status

`UNKNOWN` · `PENDING` · `SETTLED` · `REVERSED`

`UNKNOWN` is a legitimate state: Evidence may not reveal whether an event is an
authorization or a settlement.

There is no `REFUNDED` financial status — a refund is another Transaction related to
the original.

There is no `CONFIRMED` state. The combination of Evidence provenance, active Claims,
reconciliation state, financial status, and confidence is more expressive than a
`confirmed = true` flag.

---

## 6. Reconciliation

> Do these two Claims / pieces of Evidence represent the same real-world Transaction?

BillyCore compares six primary signals.

### 1. Merchant / Counterparty

The raw descriptor from Evidence is preserved. A normalized representation may also
exist.

These may all refer to one merchant:

```
AMAZON · AMZN · AMZN MX · Amazon Marketplace
```

Naive "shares some characters" logic is not sufficient. Normalization, aliasing, and
similarity may be used, but the exact algorithm is not a domain commitment. The
AI-assisted implementation belongs to BillyAgent.

### 2. Time

Close timestamps strengthen a candidate. **The reconciliation time window is not yet
decided** — see Open Questions. No arbitrary number of days is assumed here.

### 3. Amount + Currency

Same amount and currency are a strong signal. BillyCore does not invent tolerances for
tips, exchange-rate movement, or authorization adjustments. Open Questions.

### 4. Account

Matching known accounts strongly supports reconciliation. If both accounts are known
and strongly contradict, automatic reconciliation is normally prevented.

**Missing account information is not a contradiction.**

### 5. Direction

Matching direction strongly supports reconciliation. Opposite directions strongly
contradict "same Transaction":

```
800 MXN OUTFLOW  vs  800 MXN INFLOW
```

These should not normally become one Transaction. They may instead represent a
purchase / refund relationship.

### 6. Financial Status

Compatible transitions strengthen a candidate — `PENDING → SETTLED` is strong evidence
that two observations describe the same Transaction.

Two `SETTLED` observations with the same merchant and amount are *less* informative:
the user may simply have bought the same thing twice.

### Missing information

Each field comparison is classified as:

| Comparison | Effect |
|---|---|
| `MATCH` | Strengthens the candidate. |
| `MISSING` | Not a contradiction. Simply makes the match weaker. |
| `CONTRADICTION` | Weakens the candidate. Strong contradictions on important fields prevent automatic reconciliation. |

Contradictions do not all carry equal weight; they are evaluated in the context of
their field.

Potentially strong contradictions: a different known account, opposite direction,
incompatible currency, materially incompatible amount.

Weaker or context-dependent differences: `AMZN` vs `Amazon`, timestamps with some
delay, `UNKNOWN` vs `SETTLED` status.

Rigid numerical weights are not introduced until the domain actually requires them.

### Reconciliation outcomes

| Outcome | Meaning |
|---|---|
| `MATCH` | Enough compatible evidence to safely treat the observations as the same Transaction. |
| `NO_MATCH` | Enough evidence or contradiction to determine they are different Transactions. |
| `AMBIGUOUS` | BillyCore cannot safely determine either. |

> **Critical rule: `AMBIGUOUS` observations are never automatically merged.**

They stay separate until new Evidence appears, BillyAgent proposes a resolution, or a
human resolves the ambiguity.

BillyCore enforces the domain invariants in all cases. **BillyAgent cannot bypass
BillyCore merely because an LLM believes two observations match.**

#### What a MATCH does, and how BillyCore reaches one

A `MATCH` **merges** the two Transactions: the smaller id survives, the other becomes
`SUPERSEDED` and points at it, and the survivor takes the union of both Evidence so the
two-source fact is kept (D70). The candidate is recorded either way, so the decision is
auditable and idempotent by its unique pair.

BillyCore reaches a `MATCH` only through an **exact key**, never a fuzzy score:

- the **tracking key** — a shared SPEI `Clave de rastreo` is the same movement (D36);
- an **exact composite key** where no tracking key is shared — equal amount, currency,
  direction and party (the merchant, or the counterparty where a Source fills that
  field), on the same calendar day in a fixed UTC−6 — and only when it holds for exactly
  one Transaction on each of two Sources (D72).

Everything else stays `AMBIGUOUS`. The time window and the amount tolerance of §2 and §3
remain open questions, so BillyCore does not merge on them: a weaker, LLM-assisted match
is BillyAgent's to propose, and BillyCore still enforces the invariants on it.

---

## 7. Uncertainty & Confidence

Claim confidence and reconciliation uncertainty are separate concerns.

### Claim confidence

Confidence preferably exists at **field level**:

```
amount     = 800 MXN     HIGH
currency   = MXN         HIGH
merchant   = Amazon      MEDIUM
account    = ****1234    LOW
direction  = OUTFLOW     HIGH
```

A single Claim-level number such as `confidence = 0.84` is not expressive enough,
because different fields carry different certainty.

Domain values: `HIGH` · `MEDIUM` · `LOW`.

### UNKNOWN vs LOW confidence

These are different, and must stay different.

- `account = UNKNOWN` — **Billy has no Claim for this value.**
- `account = ****1234, confidence = LOW` — **Billy has a Claim, with weak support.**

> Missing information is not the same as low confidence.

### Reconciliation uncertainty

Reconciliation resolves the field comparisons (`MATCH` / `MISSING` / `CONTRADICTION`)
into an outcome (`MATCH` / `NO_MATCH` / `AMBIGUOUS`).

Everything is not forced into a single numerical reconciliation score in v1. A
numerical score may exist inside BillyAgent later; BillyCore's vocabulary stays
understandable and explainable.

### Principles

- Missing information weakens certainty; it is not a contradiction.
- Low confidence means Billy has a belief with weak support. `UNKNOWN` means Billy has
  no belief.
- Strong contradictions prevent automatic reconciliation.
- When BillyCore cannot safely decide, it returns `AMBIGUOUS` and preserves the
  separate observations.

---

## 8. Domain Events

Only events that carry a meaningful business fact. Not every state mutation is an
event. Candidates whose necessity is unclear are in Open Questions.

### EvidenceIngested

**What happened:** Billy recorded a new source artifact.

**Why others care:** it is the trigger for extraction and, downstream, reconciliation.
It is also the audit anchor for everything derived from it.

**Carries:** `evidenceId`, `sourceReference`, observed timestamp.

### ClaimActivated

**What happened:** Billy's current usable interpretation of some Evidence changed —
either a first Claim became `ACTIVE`, or a new Claim replaced an existing one.

**Why others care:** the interpretation consumers read has changed. When it carries a
superseded id, it is also the provenance record of the change.

**Carries:** `claimId`, `evidenceIds`, `interpretationId`, `supersedesInterpretationId`
(optional), `supersededClaimId` (optional). A Claim is activated as a member of an
Interpretation, so the event names the reading it belongs to and the one it replaced.

### TransactionCreated

**What happened:** Billy now believes a financial event exists.

**Why others care:** it is the first appearance of a Transaction in any consumer's
view of the data.

**Carries:** `transactionId`, originating `evidenceId`.

### TransactionReconciled

**What happened:** two observations were determined to represent the same Transaction.

**Why others care:** a consumer's earlier view may have contained what now turns out
to be one event, not two.

**Carries:** the surviving `transactionId`, the superseded `transactionId`, and the
basis — `tracking_key` or `composite` (D72). It holds no amounts: the event log does not
keep the finances of a person (SECURITY.md).

### ReconciliationAmbiguous

**What happened:** BillyCore could not safely decide, and deliberately left the
observations separate.

**Why others care:** this is the queue that BillyAgent or a human works from. Without
it, ambiguity is invisible.

**Carries:** the candidate ids, and the field comparisons that produced `AMBIGUOUS`.

### TransactionFinancialStatusChanged

**What happened:** for example `PENDING → SETTLED`, or a reversal.

**Why others care:** pending and settled money mean different things to anything
reporting balances.

**Carries:** `transactionId`, previous status, new status, causing `evidenceId`.

---

## 9. Domain Boundaries

### BillyCore owns

- The financial domain model
- Evidence provenance
- Claims
- Transactions
- Money rules
- Lifecycle and domain states
- Deterministic reconciliation rules
- Invariants
- Ambiguity representation
- Validation of proposed reconciliation decisions
- Domain events

### BillyCore does not own

**LLM inference.** BillyCore does not call an LLM to decide what an email means. That
belongs to BillyAgent or to application / infrastructure layers. BillyAgent may
*propose* Claims and reconciliation decisions; **BillyCore validates them.**

**AI / model quality judgments.** No prompts, model names, Claude-specific or
GPT-specific behavior, token logic, embeddings implementation, or model-provider
confidence semantics inside BillyCore.

**Email connectivity.** IMAP / Gmail connection, polling, retries, fetching, and
authentication are infrastructure. They may *produce* Evidence; they are not domain
logic.

**SAT-specific semantics.** Tax-specific logic does not leak into the generic domain
unless it proves to be universal financial behavior. BillySat depends on BillyCore,
never the reverse.

**Presentation.** UI wording, API presentation, dashboards, and formatting are not
domain logic.

---

## 10. Open Questions

1. What exact time window should reconciliation use?
2. How should merchant normalization / alias detection work?
3. Should different amounts ever reconcile automatically — tips, authorization
   adjustments, currency conversions?
4. How should transactions across currencies be handled?
5. When should Account graduate from `AccountIdentifier` into a real entity?
6. Should BillyCore ever use numerical confidence internally, or only qualitative
   confidence?
7. What exact contradictions are strong enough to force `NO_MATCH`?
8. Should some contradictions only force `AMBIGUOUS` instead?
9. How are relationships between Transactions represented — refund, partial refund,
   transfer, reversal?
10. Which domain events are actually needed by v1?
11. How should manual / human reconciliation resolutions be represented and audited?
12. How should BillyAgent's proposed resolutions be distinguished from deterministic
    BillyCore resolutions?

Discovered while writing this document:

13. Is a Claim derived from exactly one piece of Evidence, or may one Claim draw on
    several? Section 4 assumes provenance to "the Evidence it was derived from",
    singular — this has not actually been decided.
14. What makes two pieces of Evidence *the same artifact*? The same email fetched
    twice must not become two pieces of Evidence and then two Transactions. This is an
    identity question, distinct from reconciliation.
15. When reconciliation yields `MATCH`, does one Transaction absorb the other, or does
    a new Transaction supersede both? Section 8 assumes a "surviving transactionId"
    without that being settled.
16. Are `ClaimProposed`, `ClaimSuperseded`, and `ClaimRejected` needed as separate
    events, or is `ClaimActivated` carrying `supersededClaimId` sufficient?
17. Is `ConflictDetected` a distinct event, or is it the same fact as
    `ReconciliationAmbiguous` plus the `CONFLICTED` reconciliation state?
18. What causes a Transaction to enter the `CONFLICTED` reconciliation state, and how
    does it leave?
19. May Evidence ever be deleted — for example when a user revokes a Source or deletes
    the underlying email — and what happens to the Transactions that depend on it for
    provenance?
