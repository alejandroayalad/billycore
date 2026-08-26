-- 003_active_claim — at most one ACTIVE Claim per piece of Evidence.
--
-- This is the constraint that makes re-running extraction idempotent, and it is
-- deliberately a *constraint* rather than a code path that remembers to check.
-- M1's ingestion is idempotent because `UNIQUE (source_id, source_reference)`
-- says so; nothing about extraction was, until this.
--
-- The duplicate it prevents is real and does not need a crash to happen. A pass
-- claims a row with a one-minute lease, takes longer than the lease, and a
-- second pass claims the same row and interprets it too. Both then write a
-- Claim. The stage guard in ClaimRepository.Save does not help: the second
-- UPDATE matches no row, and the second Claim lands anyway — two interpretations
-- of one artifact, each looking exactly as legitimate as the other, and no rule
-- for which one a Transaction should be built from.
--
-- `transactions` and `transaction_evidence` are still deliberately absent, and
-- the test asserting it still holds. They were expected to be 003; they are 004.
-- A table is created when code writes to it, and nothing writes to those yet.

-- The active interpretation of one artifact.
--
-- The primary key on evidence_id *is* the invariant: DOMAIN.md §5 says ACTIVE
-- means "the Claim Billy currently uses", and two of those for one artifact is
-- not a richer answer, it is the absence of one. Claims still accumulate freely
-- in `claims` — superseded and rejected ones are never deleted (DOMAIN.md §4) —
-- and this table names which single one is live.
--
-- A row exists only while a Claim is ACTIVE, which is what keeps the proposal
-- endpoints of D11/D12 working. An outside proposer POSTing a competing
-- interpretation of the same artifact writes a PROPOSED Claim, takes no row
-- here, and conflicts with nothing. It collides only if it is activated, and at
-- that point colliding is correct: activating a second interpretation means
-- superseding the first, which is exactly what ClaimActivated's
-- supersededClaimId records.
CREATE TABLE evidence_active_claim (
    evidence_id  TEXT PRIMARY KEY NOT NULL,
    claim_id     TEXT NOT NULL,
    FOREIGN KEY (evidence_id)
        REFERENCES evidence(id)
        ON DELETE RESTRICT,
    FOREIGN KEY (claim_id)
        REFERENCES claims(id)
        ON DELETE RESTRICT
) STRICT;

-- The other direction: every artifact one Claim is the active interpretation of.
-- Supersession reads this to move the pointers when a better Claim replaces an
-- older one, and it is not covered by the primary key above.
CREATE INDEX idx_evidence_active_claim_by_claim
ON evidence_active_claim(claim_id);
