-- 009_reconciliation — the candidate that pairs two Transactions (D69).
--
-- A candidate joins two Transactions and records a status. left_ref and
-- right_ref are transactions.id, because a Transaction carries every signal
-- DOMAIN.md §6 compares. DATA_MODEL.md §9 draws this table.

-- Status is MATCH, NO_MATCH or AMBIGUOUS (DOMAIN.md §6). It is TEXT with no
-- CHECK: the domain owns that vocabulary, as 004 kept direction there.
--
-- The pair is canonical, left_ref < right_ref, so one unordered pair has one
-- row. UNIQUE on the pair is the idempotency: a re-run finds the same pair and
-- writes nothing, the way every other pass is a no-op by constraint.
CREATE TABLE reconciliation_candidate (
    id          TEXT PRIMARY KEY NOT NULL,
    left_ref    TEXT NOT NULL,
    right_ref   TEXT NOT NULL,
    status      TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    UNIQUE (left_ref, right_ref),
    CHECK (left_ref < right_ref),
    FOREIGN KEY (left_ref)
        REFERENCES transactions(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (right_ref)
        REFERENCES transactions(id)
        ON DELETE RESTRICT
) STRICT;

-- The UNIQUE index covers a lookup that starts at left_ref. This one covers the
-- other direction, so a reader can find each candidate a Transaction is in.
CREATE INDEX idx_reconciliation_candidate_by_right
ON reconciliation_candidate(right_ref);
