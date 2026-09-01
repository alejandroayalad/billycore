-- 005_interpretations — one artifact, one active interpretation, many Claims.
--
-- Migration 003 allowed one active Claim for each artifact. That shape works for
-- an email and fails for a bank statement, which holds many movements. D46 and
-- D47 give the full argument.

-- One row for each pass that a parser made over one artifact.
--
-- superseded_by_interpretation_id points from the old set to the new one, so
-- Billy can report what it believes now and what it believed before. The
-- lineage is for each set, because two readings can differ in size (D47).
CREATE TABLE interpretations (
    id                               TEXT PRIMARY KEY NOT NULL,
    evidence_id                      TEXT NOT NULL,
    superseded_by_interpretation_id  TEXT,
    created_at                       TEXT NOT NULL,
    FOREIGN KEY (evidence_id)
        REFERENCES evidence(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (superseded_by_interpretation_id)
        REFERENCES interpretations(id)
        ON DELETE RESTRICT
) STRICT;

-- Membership: the Claims that one reading holds.
--
-- A join table, and not an interpretation_id column on claims. That column would
-- make POST /v1/claims invent an id for one Claim from outside (D11, D12). The
-- join table keeps the property from D38: a PROPOSED Claim takes no slot.
CREATE TABLE interpretation_claims (
    interpretation_id  TEXT NOT NULL,
    claim_id           TEXT NOT NULL,
    PRIMARY KEY (interpretation_id, claim_id),
    FOREIGN KEY (interpretation_id)
        REFERENCES interpretations(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT
) STRICT;

-- The invariant from 003, one level higher. The primary key is the rule, and a
-- row exists only while Billy uses that interpretation.
--
-- A re-extraction moves this pointer with a compare-and-swap against the
-- interpretation that the pass expects to find. See ClaimRepository.Save.
CREATE TABLE evidence_active_interpretation (
    evidence_id        TEXT PRIMARY KEY NOT NULL,
    interpretation_id  TEXT NOT NULL,
    FOREIGN KEY (evidence_id)
        REFERENCES evidence(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (interpretation_id)
        REFERENCES interpretations(id)
        ON DELETE RESTRICT
) STRICT;

-- Carry the pointers from 003 forward: each active Claim becomes the only member
-- of its own interpretation. The id is a UUID v4 from randomblob, and a
-- migration is not the domain, so D6 permits this. created_at is the value from
-- the Claim, because the reading happened when the Claim was written.
INSERT INTO interpretations (id, evidence_id, superseded_by_interpretation_id, created_at)
SELECT
    lower(hex(randomblob(4))) || '-' ||
    lower(hex(randomblob(2))) || '-4' ||
    substr(lower(hex(randomblob(2))), 2) || '-' ||
    substr('89ab', 1 + (abs(random()) % 4), 1) ||
    substr(lower(hex(randomblob(2))), 2) || '-' ||
    lower(hex(randomblob(6))),
    eac.evidence_id,
    NULL,
    c.created_at
FROM evidence_active_claim eac
JOIN claims c ON c.id = eac.claim_id;

INSERT INTO interpretation_claims (interpretation_id, claim_id)
SELECT i.id, eac.claim_id
FROM evidence_active_claim eac
JOIN interpretations i ON i.evidence_id = eac.evidence_id;

INSERT INTO evidence_active_interpretation (evidence_id, interpretation_id)
SELECT i.evidence_id, i.id FROM interpretations i;

-- The table from 003 has no more to say. Its rows are above, in a shape that
-- also fits a statement.
DROP TABLE evidence_active_claim;

-- D49 — the lifecycle of a Transaction. It is separate from
-- reconciliation_state and from financial_status, because each column answers
-- one question. The DEFAULT fills the rows from before this migration, and each
-- of them comes from a Claim that is still active.
ALTER TABLE transactions ADD COLUMN transaction_state TEXT NOT NULL DEFAULT 'ACTIVE';

-- No code writes this column yet. When a reading replaces another one, the same
-- commit retires the Transactions of the old set, but their replacements do not
-- exist at that moment. The two sets can also differ in size, so a pointer by
-- position would be the row lineage that D47 refuses.
ALTER TABLE transactions ADD COLUMN superseded_by_transaction_id TEXT
    REFERENCES transactions(id) ON DELETE RESTRICT;

-- DATA_MODEL.md §5 — the cursor for GET /v1/transactions, now that each read of
-- the table filters to the rows that Billy stands behind.
-- idx_transactions_cursor stays, because an audit reads the full history.
CREATE INDEX idx_transactions_active_cursor
ON transactions(transaction_state, occurred_at DESC, id DESC);

-- The directions that the primary keys above do not cover: each reading of one
-- artifact, the interpretation of one Claim, and the artifact that names one
-- interpretation.
CREATE INDEX idx_interpretations_by_evidence
ON interpretations(evidence_id, created_at);

CREATE INDEX idx_interpretation_claims_by_claim
ON interpretation_claims(claim_id);

CREATE INDEX idx_evidence_active_interpretation_by_interpretation
ON evidence_active_interpretation(interpretation_id);
