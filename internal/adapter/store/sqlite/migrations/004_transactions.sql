-- 004_transactions — Transactions, their provenance to Evidence, and the slot
-- that makes building one idempotent.
--
-- DATA_MODEL.md §4.5 and §4.6. These tables were expected to be 003 and moved
-- when the active-claim constraint took that number (D38). They arrive now, and
-- only now, because this is the migration where code finally writes to them:
-- `internal/app/reconcile.go` reads ACTIVE Claims and builds Transactions from
-- them. A table created before that code existed would have been a schema
-- nobody had exercised.
--
-- Nothing here validates a domain value. `direction`, `financial_status` and
-- `reconciliation_state` are TEXT with no CHECK, for the reason 002 gave: the
-- domain owns those rules (DATA_MODEL.md §2, D6), and a second copy in the
-- schema is a second place for them to drift.

-- DATA_MODEL.md §4.5.
--
-- **Money is nullable, and its two halves are nullable together.** A Transaction
-- may exist without Money where the Evidence does not support an amount; where
-- `amount_minor` exists, `currency` must too. That pairing is a domain invariant
-- (DOMAIN.md §4: currency is required wherever Money exists) and is enforced in
-- `domain.NewTransaction`, not by a CHECK here — recording an amount Billy
-- cannot support is a fabricated financial fact, and deciding that is the
-- domain's job in both of the places a Transaction can come from.
--
-- **`occurred_at` is NOT NULL and always populated.** Where the artifact stated
-- an event time, that is the value; where it did not — all 90 `¡Recibimos tu
-- pago!` card payments state none — it is the earliest `observed_at` of the
-- supporting Evidence. DATA_MODEL.md §4.5 requires that fallback to be computed
-- before the write and never inside a query, so this column can be trusted as a
-- pagination cursor without a COALESCE anywhere.
CREATE TABLE transactions (
    id                      TEXT PRIMARY KEY NOT NULL,
    amount_minor            INTEGER,
    currency                TEXT,
    merchant                TEXT,
    account_identifier      TEXT,
    direction               TEXT NOT NULL,
    financial_status        TEXT NOT NULL,
    reconciliation_state    TEXT NOT NULL,
    occurred_at             TEXT NOT NULL,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL
) STRICT;

-- DATA_MODEL.md §4.6. A join table, so the schema takes no position on how many
-- artifacts support one Transaction; the domain requires at least one, and
-- SQLite does not try to enforce that cross-table rule.
--
-- There is deliberately **no `UNIQUE (evidence_id)`** here. It would read as
-- "one artifact yields at most one Transaction", which is true of every one of
-- the 1,044 emails and false of the bank statement D27 put in the MVP: one PDF,
-- many movements. A constraint that has to be dropped one slice later is not the
-- natural key — see `claim_transaction` below (D41).
CREATE TABLE transaction_evidence (
    transaction_id    TEXT NOT NULL,
    evidence_id       TEXT NOT NULL,
    PRIMARY KEY (transaction_id, evidence_id),
    FOREIGN KEY (transaction_id)
        REFERENCES transactions(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (evidence_id)
        REFERENCES evidence(id)
        ON DELETE RESTRICT
) STRICT;

-- D41 — the Transaction Billy built from one Claim.
--
-- The primary key on `claim_id` *is* the idempotency, in the same shape and for
-- the same reason as `evidence_active_claim` in 003: re-running reconciliation
-- must not produce two Transactions from one Claim, and it must not do so
-- because a constraint says so rather than because a code path remembered to
-- check. The race is identical to the one 003 closed — a pass runs longer than
-- its lease, a second pass claims the same row, and both build. The stage guard
-- does not help, because the second UPDATE matches no row and the Transaction
-- lands anyway: two rows of money for one movement, each as legitimate-looking
-- as the other.
--
-- It keys on the *Claim* rather than the Evidence because the closed eight-name
-- field vocabulary already means one Claim describes exactly one movement,
-- whatever the artifact it came from held.
--
-- Not modelled as a `source_claim_id` column on `transactions`: DATA_MODEL.md
-- §4.5 writes that table's columns down, and §6 lists the foreign keys that
-- exist without a claims→transactions edge among them. A slot table adds
-- neither, and keeps this what it is — an infrastructure fact about which pass
-- won a race, not provenance. Provenance is to Evidence (DOMAIN.md §4), and it
-- lives in `transaction_evidence`.
CREATE TABLE claim_transaction (
    claim_id        TEXT PRIMARY KEY NOT NULL,
    transaction_id  TEXT NOT NULL,
    FOREIGN KEY (claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (transaction_id)
        REFERENCES transactions(id)
        ON DELETE RESTRICT
) STRICT;

-- DATA_MODEL.md §5 — the cursor `GET /v1/transactions` paginates on.
CREATE INDEX idx_transactions_cursor
ON transactions(occurred_at DESC, id DESC);

-- DATA_MODEL.md §5 — finding the Transaction a piece of Evidence supports.
-- transaction_evidence's primary key already indexes transaction_id, so the
-- direction that is missing is the other one.
CREATE INDEX idx_transaction_evidence_by_evidence
ON transaction_evidence(evidence_id, transaction_id);

-- The other direction on the slot, for the same reason 003 indexed its own:
-- retiring a Transaction needs to find the slot pointing at it, and the primary
-- key above does not cover that.
CREATE INDEX idx_claim_transaction_by_transaction
ON claim_transaction(transaction_id);
