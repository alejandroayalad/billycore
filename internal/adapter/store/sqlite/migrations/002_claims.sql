-- 002_claims — Claims, their fields, and their provenance to Evidence.
--
-- DATA_MODEL.md §4.2 through §4.4. `transactions` and `transaction_evidence`
-- are specified in §4.5 and §4.6 and are deliberately *not* created here, for
-- the same reason 001 left these three out: a table is created when code writes
-- to it, not when a document describes it. They arrive in 003, with the code
-- that fills them.
--
-- Nothing here validates a domain value. `state`, `field_name` and
-- `confidence` are all TEXT with no CHECK, deliberately: the domain owns those
-- rules (DATA_MODEL.md §2), and a second copy in the schema is a second place
-- for them to drift. The CHECK that does exist, on claim_fields, protects the
-- physical representation rather than any financial meaning.

-- DATA_MODEL.md §4.2.
--
-- `superseded_by_claim_id` points from the old Claim to the new one, which is
-- the direction the API contract reads. ON DELETE RESTRICT everywhere in this
-- migration: DATA_MODEL.md §6's cascade policy is that nothing supporting a
-- financial fact disappears because something else was removed.
CREATE TABLE claims (
    id                      TEXT PRIMARY KEY NOT NULL,
    state                   TEXT NOT NULL,
    superseded_by_claim_id  TEXT,
    created_at              TEXT NOT NULL,
    updated_at              TEXT NOT NULL,
    FOREIGN KEY (superseded_by_claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT
) STRICT;

-- DATA_MODEL.md §4.3. A join table rather than an evidence_id column on claims,
-- so the schema takes no position on whether one Claim may rest on several
-- artifacts. The domain decides the cardinality; today it requires at least one.
CREATE TABLE claim_evidence (
    claim_id       TEXT NOT NULL,
    evidence_id    TEXT NOT NULL,
    PRIMARY KEY (claim_id, evidence_id),
    FOREIGN KEY (claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (evidence_id)
        REFERENCES evidence(id)
        ON DELETE RESTRICT
) STRICT;

-- DATA_MODEL.md §4.4, plus occurred_at per D33 — seven field names, all of them
-- the domain's business rather than the schema's.
--
-- If Billy has no Claim for a field, no row exists. That is what keeps "Billy
-- has no belief about the account" distinguishable from "Billy believes the
-- account is 1234, weakly" (DOMAIN.md §7), and it is why there is no NOT NULL
-- default value anywhere here to fill a gap with a plausible-looking zero.
CREATE TABLE claim_fields (
    claim_id       TEXT NOT NULL,
    field_name     TEXT NOT NULL,
    value_int      INTEGER,
    value_text     TEXT,
    confidence     TEXT NOT NULL,
    PRIMARY KEY (claim_id, field_name),
    FOREIGN KEY (claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT,
    CHECK (
        (value_int IS NOT NULL AND value_text IS NULL)
        OR
        (value_int IS NULL AND value_text IS NOT NULL)
    )
) STRICT;

-- DATA_MODEL.md §5 — reading a Claim's fields, and finding the Claims derived
-- from one artifact. claim_evidence's primary key already indexes claim_id, so
-- the index that is missing is the other direction.
CREATE INDEX idx_claim_evidence_by_evidence
ON claim_evidence(evidence_id);
