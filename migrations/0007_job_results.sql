-- 0007_job_results.sql
-- A job that produces something — an export archive, say — needs somewhere to
-- record what it produced, so the operator can download it afterwards.
--
-- This is a separate migration rather than an edit to 0006 because 0006 has
-- already been applied. A financial database is repaired forward
-- (ARCHITECTURE.md, Migrations).

ALTER TABLE background_jobs
    ADD COLUMN result jsonb;

COMMENT ON COLUMN background_jobs.result IS
    'What a successful job produced: for an export, its filename, stored path, SHA-256 and size.';
