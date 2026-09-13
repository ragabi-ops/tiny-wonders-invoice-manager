-- 0008_business_logo.sql
-- The business logo, printed at the top of every document.
--
-- The logo is stored like any other artifact: a file on disk plus its SHA-256.
-- A document's snapshot records the path and hash of the logo *as it was at
-- issuance*, so re-rendering a document from two years ago shows the logo the
-- business used then, not the one it uses now (plan.md 11).
--
-- That only holds if logo files are never overwritten, so each upload is
-- written to its own path and previous files are retained.

ALTER TABLE business_profile
    ADD COLUMN logo_path         text,
    ADD COLUMN logo_content_type text,
    ADD COLUMN logo_sha256       text CHECK (logo_sha256 IS NULL OR logo_sha256 ~ '^[0-9a-f]{64}$'),
    ADD COLUMN logo_byte_size    bigint CHECK (logo_byte_size IS NULL OR logo_byte_size > 0),
    ADD COLUMN logo_uploaded_at  timestamptz;

COMMENT ON COLUMN business_profile.logo_path IS
    'Storage path of the current logo. Previous logo files are retained so historical documents keep rendering as issued.';
