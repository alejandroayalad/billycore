-- 007_unread_rows — how partial a reading was.
--
-- D59: a statement holds about 180 movements that have nothing to do with each
-- other, so a row no shape reads is skipped and the rest are written together.
-- A reading that says nothing about what it skipped under-reports in silence,
-- and PRODUCT.md allows BillyCore to be incomplete and never unsupported.

-- The count of movements that the parser did not read.
--
-- NOT NULL with a default of zero, because every reading knows this number: it
-- is zero when the parser read the complete artifact. The default also fills
-- the 800 email interpretations from before this migration, and zero is the
-- true value for each of them — the four Nu templates are each a receipt for
-- one movement, so a reading of an email reads all of it or none of it.
--
-- A count above zero is a reading that asks to be looked at. Re-extraction is
-- how it is corrected (D48), and it needs no schema of its own.
ALTER TABLE interpretations ADD COLUMN unread_rows INTEGER NOT NULL DEFAULT 0;
