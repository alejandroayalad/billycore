-- 008_counterparty — who was on the other side of a movement.
--
-- D61 gives the Claim vocabulary `counterparty`, and the Transaction had no
-- column to keep it in. A statement SPEI names a person and an institution, and
-- neither one is a merchant, so reconciliation would have read the field and
-- then dropped it.

-- Nullable, because most movements state no counterparty: a card purchase names
-- a shop, which is the merchant, and 90 card payments name nobody. NULL is
-- absence and never a counterparty named nothing (DATA_MODEL.md §2).
--
-- The reserved counterparty value marks a movement between accounts of one user
-- (D62, D65). `domain.CounterpartySelf` declares it and the domain validates the
-- namespace; this column stores text, for the reason 004 gave.
--
-- Existing rows take NULL, which is true of each of them: no email template
-- yields a counterparty today.
ALTER TABLE transactions ADD COLUMN counterparty TEXT;
