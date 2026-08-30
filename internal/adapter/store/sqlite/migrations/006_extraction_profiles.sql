-- 006_extraction_profiles — the reading contract of each artifact.
--
-- A profile names the parser that reads a Source's artifacts. The two columns
-- below answer two different questions, and that is why there are two.

-- The dispatch input. It is nullable, because it is configuration and not a
-- fact about the artifact: a Source that names no profile records rows that
-- fail extraction with a reason that says so. A re-extraction can correct a
-- profile that was wrong, which is the whole reason it is not on the immutable
-- Evidence object (D48).
ALTER TABLE evidence ADD COLUMN extraction_profile TEXT;

-- Every stored artifact is a Nu email. The 1,044 rows come from one Gmail
-- Source, and the four templates of NU_EMAIL_V1 read 800 of them
-- (CONTEXT.md §3.1). The WHERE guard keeps this statement from touching a row
-- that a later BillyCore recorded with a profile of its own.
UPDATE evidence SET extraction_profile = 'NU_EMAIL_V1' WHERE extraction_profile IS NULL;

-- The record of the parser that made the reading. It is NOT NULL, because a
-- reading always has a parser that made it, and it never changes: D47's lineage
-- keeps the readings that came before. The DEFAULT fills the 800 rows from
-- before this migration, for the reason above. ClaimRepository.Save writes the
-- column, so the default applies to no row that BillyCore writes after this.
ALTER TABLE interpretations ADD COLUMN extraction_profile TEXT NOT NULL DEFAULT 'NU_EMAIL_V1';
