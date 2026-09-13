-- 0006_operations.sql
-- Phase 5: expenses and their attachments, the background job queue, and the
-- delivery history (plan.md 13, 14, 16, 17).
--
-- The rule that shapes delivery: a failed send must NEVER roll back a
-- successfully issued document (plan.md 14). Delivery therefore happens after
-- issuance, in its own transaction, driven by a queue — never inside the
-- issuance transaction.

-- -------------------------------------------------------------- expenses --

-- Expense tracking supports the business and its accountant. It is not
-- double-entry bookkeeping, and it deliberately does not compute VAT input
-- deduction: an exempt dealer has none (plan.md 13).
CREATE TABLE expenses (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    supplier     text   NOT NULL CHECK (length(btrim(supplier)) > 0),
    expense_date date   NOT NULL,
    amount_agorot bigint NOT NULL CHECK (amount_agorot > 0),
    category     text   NOT NULL DEFAULT '',

    -- The same closed set payments use, so "how did money move" reads the same
    -- in both directions.
    payment_method text NOT NULL DEFAULT 'OTHER' CHECK (payment_method IN (
        'CASH', 'BANK_TRANSFER', 'CREDIT_CARD', 'BIT', 'PAYBOX', 'CHECK', 'OTHER'
    )),
    reference text NOT NULL DEFAULT '',
    notes     text NOT NULL DEFAULT '',

    activity_id uuid REFERENCES activities (id) ON DELETE SET NULL,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,

    search_text text GENERATED ALWAYS AS (
        supplier || ' ' || category || ' ' || reference || ' ' || notes
    ) STORED
);

CREATE INDEX expenses_date_idx      ON expenses (expense_date DESC);
CREATE INDEX expenses_category_idx  ON expenses (category, expense_date DESC) WHERE category <> '';
CREATE INDEX expenses_activity_idx  ON expenses (activity_id) WHERE activity_id IS NOT NULL;
CREATE INDEX expenses_search_idx    ON expenses USING gin (search_text gin_trgm_ops);

CREATE TRIGGER expenses_set_updated_at
    BEFORE UPDATE ON expenses
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ----------------------------------------------------------- attachments --

-- Uploaded files: receipts photographed at the till, supplier invoices as PDFs.
-- Stored on disk under STORAGE_DIR with their SHA-256, exactly like issued
-- document PDFs, and covered by the same backups (plan.md 13, 18).
CREATE TABLE attachments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Polymorphic owner. Only expenses attach files in v1; the shape is here so
    -- the next owner does not need a new table.
    entity_type text NOT NULL CHECK (entity_type IN ('expense')),
    entity_id   uuid NOT NULL,

    -- What the uploader called it, shown in the UI and used on export. It never
    -- determines where the file is written.
    original_filename text NOT NULL,
    content_type      text NOT NULL,
    byte_size         bigint NOT NULL CHECK (byte_size > 0),
    sha256            text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_path      text NOT NULL,

    uploaded_at timestamptz NOT NULL DEFAULT now(),
    uploaded_by uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX attachments_owner_idx ON attachments (entity_type, entity_id);

-- --------------------------------------------------------- background jobs --

-- A PostgreSQL-backed queue. Workers claim with FOR UPDATE SKIP LOCKED, so two
-- instances never run the same job. Email, exports, retries and backups run
-- here; official issuance stays synchronous (plan.md 17).
CREATE TABLE background_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    kind    text  NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,

    state text NOT NULL DEFAULT 'PENDING'
        CHECK (state IN ('PENDING', 'RUNNING', 'SUCCEEDED', 'FAILED', 'CANCELLED')),

    attempts     integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    max_attempts integer NOT NULL DEFAULT 5 CHECK (max_attempts >= 1),

    -- When the job becomes eligible. Retries push this out with backoff.
    run_at      timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz,
    last_error  text,

    -- Optional deduplication: two identical enqueues collapse into one pending
    -- job rather than sending the same email twice.
    dedupe_key text,

    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL
);

-- Only one live job per dedupe key; finished ones no longer block a re-enqueue.
CREATE UNIQUE INDEX background_jobs_dedupe_key
    ON background_jobs (dedupe_key)
    WHERE dedupe_key IS NOT NULL AND state IN ('PENDING', 'RUNNING');

CREATE INDEX background_jobs_claim_idx ON background_jobs (run_at)
    WHERE state = 'PENDING';
CREATE INDEX background_jobs_state_idx ON background_jobs (state, created_at DESC);

-- ------------------------------------------------------ delivery attempts --

-- Every attempt to get a document to a customer, successful or not. This is the
-- delivery status and history plan.md 14 asks for, and the reason a failure is
-- visible rather than silent.
CREATE TABLE delivery_attempts (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,
    channel     text NOT NULL CHECK (channel IN ('EMAIL', 'WHATSAPP_LINK')),
    recipient   text NOT NULL DEFAULT '',

    state text NOT NULL CHECK (state IN ('QUEUED', 'SENT', 'FAILED')),
    error text,

    job_id uuid REFERENCES background_jobs (id) ON DELETE SET NULL,

    attempted_at timestamptz NOT NULL DEFAULT now(),
    attempted_by uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX delivery_attempts_document_idx ON delivery_attempts (document_id, attempted_at DESC);
CREATE INDEX delivery_attempts_failed_idx   ON delivery_attempts (attempted_at DESC)
    WHERE state = 'FAILED';

-- Delivery history is evidence of what was sent and when; it is not rewritten.
CREATE TRIGGER delivery_attempts_append_only
    BEFORE UPDATE OR DELETE ON delivery_attempts
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();
