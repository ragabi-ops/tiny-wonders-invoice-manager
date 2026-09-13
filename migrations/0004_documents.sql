-- 0004_documents.sql
-- Phase 3: documents, their lines, the numbering sequences, the relations
-- between documents, and the idempotency store.
--
-- The guarantees this migration enforces IN THE DATABASE, not merely in Go
-- (plan.md 3.1-3.4, 10, 11):
--
--   * an issued document's official number is unique per type and series
--   * an issued document cannot be edited or deleted; the only permitted
--     transition is ISSUED -> CANCELLED, which adds cancellation fields and
--     changes nothing else
--   * an issued document must carry its number, issue time, snapshot and PDF
--     hash — a row cannot claim to be issued without its artifacts
--   * money is integer agorot; quantities are integer thousandths

-- ------------------------------------------------------------- sequences --

-- One counter per (document_type, series). Allocation happens inside the
-- issuance transaction, so concurrent issuance serializes on this row
-- (plan.md 10).
CREATE TABLE document_sequences (
    document_type text   NOT NULL,
    series        text   NOT NULL,
    next_number   bigint NOT NULL DEFAULT 1 CHECK (next_number >= 1),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (document_type, series)
);

COMMENT ON TABLE document_sequences IS
    'Official numbering. next_number is the number the NEXT issuance will take.';

-- ------------------------------------------------------------- documents --

CREATE TABLE documents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    document_type text NOT NULL
        CHECK (document_type IN ('TRANSACTION_INVOICE', 'PAYMENT_REQUEST', 'RECEIPT')),
    state         text NOT NULL DEFAULT 'DRAFT'
        CHECK (state IN ('DRAFT', 'ISSUED', 'CANCELLED')),

    -- Series separates official numbering from the numbering used while the
    -- compliance gate is closed, so a test document can never consume an
    -- official number (plan.md 2, 10).
    series text   NOT NULL DEFAULT 'A' CHECK (length(btrim(series)) > 0),
    number bigint CHECK (number IS NULL OR number >= 1),

    customer_id uuid NOT NULL REFERENCES customers (id) ON DELETE RESTRICT,
    activity_id uuid          REFERENCES activities (id) ON DELETE SET NULL,

    -- Business dates, already interpreted in Asia/Jerusalem.
    document_date date NOT NULL,
    due_date      date,

    currency        text   NOT NULL DEFAULT 'ILS' CHECK (currency = 'ILS'),
    subtotal_agorot bigint NOT NULL DEFAULT 0,
    vat_agorot      bigint NOT NULL DEFAULT 0 CHECK (vat_agorot >= 0),
    total_agorot    bigint NOT NULL DEFAULT 0,

    notes         text NOT NULL DEFAULT '',
    -- Wording the compliance adapter requires on this document, captured as
    -- issued rather than re-derived later.
    required_wording text NOT NULL DEFAULT '',

    issued_at timestamptz,
    issued_by uuid REFERENCES users (id) ON DELETE SET NULL,

    cancelled_at        timestamptz,
    cancelled_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    cancellation_reason text,

    -- The exact values used at issuance: business identity, customer, lines,
    -- totals, wording. A historical document is rendered from this, never from
    -- the mutable tables (plan.md 11).
    snapshot jsonb,

    -- The stored artifact and its fingerprint, so tampering is detectable.
    pdf_path         text,
    pdf_sha256       text CHECK (pdf_sha256 IS NULL OR pdf_sha256 ~ '^[0-9a-f]{64}$'),
    pdf_bytes        bigint CHECK (pdf_bytes IS NULL OR pdf_bytes > 0),
    template_version text,

    -- True when issued while the compliance gate was closed. Such a document is
    -- watermarked, numbered in its own series, and is not a legal document.
    test_mode boolean NOT NULL DEFAULT false,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,

    -- A draft has no official number; an issued or cancelled document must have
    -- everything that makes it a document.
    CONSTRAINT documents_draft_has_no_number
        CHECK (state <> 'DRAFT' OR (number IS NULL AND issued_at IS NULL AND snapshot IS NULL)),
    CONSTRAINT documents_issued_is_complete
        CHECK (state = 'DRAFT' OR (
            number IS NOT NULL AND
            issued_at IS NOT NULL AND
            snapshot IS NOT NULL AND
            pdf_path IS NOT NULL AND
            pdf_sha256 IS NOT NULL AND
            template_version IS NOT NULL
        )),
    CONSTRAINT documents_cancelled_has_reason
        CHECK (state <> 'CANCELLED' OR (cancelled_at IS NOT NULL AND cancellation_reason IS NOT NULL)),
    CONSTRAINT documents_total_is_subtotal_plus_vat
        CHECK (total_agorot = subtotal_agorot + vat_agorot)
);

-- The core numbering guarantee: no two documents of a type and series share a
-- number. Partial, because drafts have no number (plan.md 10).
CREATE UNIQUE INDEX documents_official_number_key
    ON documents (document_type, series, number)
    WHERE number IS NOT NULL;

CREATE INDEX documents_customer_idx ON documents (customer_id, document_date DESC);
CREATE INDEX documents_state_idx    ON documents (state, document_date DESC);
CREATE INDEX documents_type_idx     ON documents (document_type, document_date DESC);
CREATE INDEX documents_activity_idx ON documents (activity_id) WHERE activity_id IS NOT NULL;
CREATE INDEX documents_issued_at_idx ON documents (issued_at DESC) WHERE issued_at IS NOT NULL;

CREATE TRIGGER documents_set_updated_at
    BEFORE UPDATE ON documents
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- --------------------------------------------------------- immutability ----

-- Issued documents are append-only. The only permitted change is the
-- ISSUED -> CANCELLED transition, which may set only the cancellation fields:
-- the original number, totals, snapshot and artifact are preserved
-- (plan.md 3.1, 9).
CREATE OR REPLACE FUNCTION documents_enforce_immutability() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.state <> 'DRAFT' THEN
            RAISE EXCEPTION 'document % is % and cannot be deleted', OLD.id, OLD.state
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    -- Drafts are freely editable; they are not documents yet.
    IF OLD.state = 'DRAFT' THEN
        RETURN NEW;
    END IF;

    IF OLD.state = 'CANCELLED' THEN
        RAISE EXCEPTION 'document % is already cancelled and cannot be changed', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    -- OLD.state = 'ISSUED' from here on.
    IF NEW.state <> 'CANCELLED' THEN
        RAISE EXCEPTION 'issued document % cannot be modified (attempted state %)', OLD.id, NEW.state
            USING ERRCODE = 'restrict_violation';
    END IF;

    IF NEW.document_type    IS DISTINCT FROM OLD.document_type
       OR NEW.series        IS DISTINCT FROM OLD.series
       OR NEW.number        IS DISTINCT FROM OLD.number
       OR NEW.customer_id   IS DISTINCT FROM OLD.customer_id
       OR NEW.activity_id   IS DISTINCT FROM OLD.activity_id
       OR NEW.document_date IS DISTINCT FROM OLD.document_date
       OR NEW.due_date      IS DISTINCT FROM OLD.due_date
       OR NEW.currency      IS DISTINCT FROM OLD.currency
       OR NEW.subtotal_agorot IS DISTINCT FROM OLD.subtotal_agorot
       OR NEW.vat_agorot      IS DISTINCT FROM OLD.vat_agorot
       OR NEW.total_agorot    IS DISTINCT FROM OLD.total_agorot
       OR NEW.notes            IS DISTINCT FROM OLD.notes
       OR NEW.required_wording IS DISTINCT FROM OLD.required_wording
       OR NEW.issued_at        IS DISTINCT FROM OLD.issued_at
       OR NEW.issued_by        IS DISTINCT FROM OLD.issued_by
       OR NEW.snapshot         IS DISTINCT FROM OLD.snapshot
       OR NEW.pdf_path         IS DISTINCT FROM OLD.pdf_path
       OR NEW.pdf_sha256       IS DISTINCT FROM OLD.pdf_sha256
       OR NEW.pdf_bytes        IS DISTINCT FROM OLD.pdf_bytes
       OR NEW.template_version IS DISTINCT FROM OLD.template_version
       OR NEW.test_mode        IS DISTINCT FROM OLD.test_mode
       OR NEW.created_at       IS DISTINCT FROM OLD.created_at
    THEN
        RAISE EXCEPTION 'cancelling document % must not change any other field', OLD.id
            USING ERRCODE = 'restrict_violation';
    END IF;

    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER documents_immutable
    BEFORE UPDATE OR DELETE ON documents
    FOR EACH ROW EXECUTE FUNCTION documents_enforce_immutability();

-- -------------------------------------------------------- document lines --

CREATE TABLE document_lines (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id uuid NOT NULL REFERENCES documents (id) ON DELETE CASCADE,
    line_number integer NOT NULL CHECK (line_number >= 1),

    -- The catalogue item this line came from, if any. Free-form lines are
    -- allowed and carry no reference (plan.md 8).
    service_id uuid REFERENCES services (id) ON DELETE SET NULL,

    description text NOT NULL CHECK (length(btrim(description)) > 0),
    unit        text NOT NULL DEFAULT '',

    -- Quantity in integer thousandths, so 1.5 hours is 1500. Exact, like money:
    -- no float ever touches a line.
    quantity_milli    bigint NOT NULL CHECK (quantity_milli > 0),
    unit_price_agorot bigint NOT NULL CHECK (unit_price_agorot >= 0),
    line_total_agorot bigint NOT NULL CHECK (line_total_agorot >= 0),

    -- Human-readable mirror of quantity_milli, for SQL and accountant exports.
    quantity numeric(18, 3) GENERATED ALWAYS AS (quantity_milli::numeric / 1000) STORED,

    created_at timestamptz NOT NULL DEFAULT now(),

    UNIQUE (document_id, line_number)
);

CREATE INDEX document_lines_document_idx ON document_lines (document_id, line_number);
CREATE INDEX document_lines_service_idx  ON document_lines (service_id) WHERE service_id IS NOT NULL;

-- Lines of an issued document are as immutable as the document itself.
CREATE OR REPLACE FUNCTION document_lines_enforce_immutability() RETURNS trigger AS $$
DECLARE
    parent_state text;
    parent_id    uuid;
BEGIN
    parent_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.document_id ELSE NEW.document_id END;

    SELECT state INTO parent_state FROM documents WHERE id = parent_id;

    -- No parent row means the document is being deleted and this is the
    -- cascade; the document's own trigger has already had its say.
    IF parent_state IS NULL OR parent_state = 'DRAFT' THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

    RAISE EXCEPTION 'lines of % document % cannot be changed', parent_state, parent_id
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER document_lines_immutable
    BEFORE INSERT OR UPDATE OR DELETE ON document_lines
    FOR EACH ROW EXECUTE FUNCTION document_lines_enforce_immutability();

-- ---------------------------------------------------- document relations --

-- How documents refer to one another: a cancellation, a correction, or the
-- receipt issued against an invoice (plan.md 9).
CREATE TABLE document_relations (
    from_document_id uuid NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,
    to_document_id   uuid NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,
    relation_type    text NOT NULL
        CHECK (relation_type IN ('CANCELS', 'CORRECTS', 'RECEIPT_FOR')),
    created_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,

    PRIMARY KEY (from_document_id, to_document_id, relation_type),
    CONSTRAINT document_relations_not_self CHECK (from_document_id <> to_document_id)
);

CREATE INDEX document_relations_to_idx ON document_relations (to_document_id);

CREATE TRIGGER document_relations_append_only
    BEFORE UPDATE OR DELETE ON document_relations
    FOR EACH ROW EXECUTE FUNCTION deny_mutation();

-- ------------------------------------------------------------ idempotency --

-- Replays of a critical POST return the original response instead of acting
-- twice (plan.md 10, 17). The fingerprint guards against a key being reused
-- for a different request.
CREATE TABLE idempotency_keys (
    scope               text NOT NULL,
    key                 text NOT NULL CHECK (length(btrim(key)) > 0),
    request_fingerprint text NOT NULL,
    user_id             uuid REFERENCES users (id) ON DELETE SET NULL,
    response_status     integer NOT NULL,
    response_body       jsonb   NOT NULL,
    entity_id           text,
    created_at          timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (scope, key)
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);
