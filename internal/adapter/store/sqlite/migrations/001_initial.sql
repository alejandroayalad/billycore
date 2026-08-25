-- 001_initial — Evidence, and the event log that records its arrival.
--
-- M1 scope: Gmail → Evidence → SQLite. `claims`, `claim_evidence`,
-- `claim_fields`, `transactions`, and `transaction_evidence` are specified in
-- DATA_MODEL.md §4 and deliberately not created here — a table is created when
-- code writes to it, not when a document describes it.

-- DATA_MODEL.md §4.1, plus `content_type` per D24.
CREATE TABLE evidence (
    id                  TEXT PRIMARY KEY NOT NULL,
    source_id           TEXT NOT NULL,
    source_type         TEXT NOT NULL,
    source_reference    TEXT NOT NULL,
    content_type        TEXT,
    raw_content         BLOB,
    observed_at         TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    processing_stage    TEXT NOT NULL,
    attempts            INTEGER NOT NULL DEFAULT 0,
    last_error          TEXT,
    locked_until        TEXT,
    UNIQUE (source_id, source_reference)
) STRICT;

-- DATA_MODEL.md §5. `UNIQUE (source_id, source_reference)` already indexes the
-- ingestion upsert, so no second index is declared for it.
CREATE INDEX idx_evidence_pipeline_scan
ON evidence(processing_stage, observed_at, id);

-- DATA_MODEL.md §4.7. Append-only: never updated, never deleted. `seq` is the
-- rowid and the cursor `GET /v1/events` will read, which does not exist yet.
-- Present in 001 rather than 002 because of D25: an event has to be written in
-- the same transaction as the change it describes, and the very first Evidence
-- rows are the audit anchor for everything derived from them later.
CREATE TABLE domain_event (
    seq         INTEGER PRIMARY KEY,
    type        TEXT NOT NULL,
    payload     TEXT NOT NULL,
    occurred_at TEXT NOT NULL
) STRICT;
