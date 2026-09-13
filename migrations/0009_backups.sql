-- 0009_backups.sql
-- Phase 6: the record of every backup run.
--
-- A backup nobody checks is not a backup. This table is what makes "when did
-- this last work?" answerable — by the readiness probe, by the dashboard, and
-- by an operator asking the question directly (plan.md 18).

CREATE TABLE backup_runs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    started_at  timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,

    state text NOT NULL DEFAULT 'RUNNING'
        CHECK (state IN ('RUNNING', 'SUCCEEDED', 'FAILED')),

    -- Where the archive landed, and what it contains.
    archive_path  text,
    archive_bytes bigint CHECK (archive_bytes IS NULL OR archive_bytes > 0),
    archive_sha256 text CHECK (archive_sha256 IS NULL OR archive_sha256 ~ '^[0-9a-f]{64}$'),
    file_count    integer CHECK (file_count IS NULL OR file_count >= 0),

    -- SCHEDULED for the daily run, MANUAL when an operator asked for one.
    trigger    text NOT NULL DEFAULT 'SCHEDULED' CHECK (trigger IN ('SCHEDULED', 'MANUAL')),
    triggered_by uuid REFERENCES users (id) ON DELETE SET NULL,

    error text
);

CREATE INDEX backup_runs_started_at_idx ON backup_runs (started_at DESC);
CREATE INDEX backup_runs_succeeded_idx  ON backup_runs (finished_at DESC)
    WHERE state = 'SUCCEEDED';

-- The history of what was backed up and when is evidence, not working state.
CREATE TRIGGER backup_runs_no_delete
    BEFORE DELETE ON backup_runs
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();
