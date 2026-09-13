-- 0005_payments.sql
-- Phase 4: payments, how they are allocated against documents, and the receipts
-- they produce (plan.md 12).
--
-- The shape of the problem: money arrives, sometimes in full, sometimes in
-- part, sometimes split across several payments, and sometimes with no invoice
-- behind it at all. A payment is therefore a record in its own right, and what
-- it settles is a separate, explicit set of allocations.
--
-- Guarantees enforced HERE, not only in Go:
--
--   * a payment that has produced a receipt can never be edited or deleted
--   * neither can its allocations
--   * a payment has at most one receipt
--   * every amount is positive integer agorot

CREATE TABLE payments (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    customer_id uuid NOT NULL REFERENCES customers (id) ON DELETE RESTRICT,

    -- Always positive. A refund is a different act with different paperwork and
    -- is not modelled as a negative payment.
    amount_agorot bigint NOT NULL CHECK (amount_agorot > 0),

    -- Business date, already interpreted in Asia/Jerusalem.
    received_at date NOT NULL,

    method text NOT NULL CHECK (method IN (
        'CASH', 'BANK_TRANSFER', 'CREDIT_CARD', 'BIT', 'PAYBOX', 'CHECK', 'OTHER'
    )),
    -- Cheque number, transfer reference, last digits of a card, and so on.
    reference text NOT NULL DEFAULT '',
    notes     text NOT NULL DEFAULT '',

    activity_id uuid REFERENCES activities (id) ON DELETE SET NULL,

    -- The receipt issued for this payment. NULL means none has been issued yet,
    -- which is a real state: money can arrive while the PDF renderer is down,
    -- and refusing to record it would be worse than recording it unreceipted.
    receipt_document_id uuid UNIQUE REFERENCES documents (id) ON DELETE RESTRICT,

    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL
);

CREATE INDEX payments_customer_idx    ON payments (customer_id, received_at DESC);
CREATE INDEX payments_received_at_idx ON payments (received_at DESC);
CREATE INDEX payments_method_idx      ON payments (method, received_at DESC);
CREATE INDEX payments_activity_idx    ON payments (activity_id) WHERE activity_id IS NOT NULL;
CREATE INDEX payments_unreceipted_idx ON payments (received_at DESC) WHERE receipt_document_id IS NULL;

CREATE TRIGGER payments_set_updated_at
    BEFORE UPDATE ON payments
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ------------------------------------------------------------ allocations --

-- What a payment settles. A payment may cover several documents, and a document
-- may be settled by several payments; the amounts are explicit rather than
-- inferred, so partial and split payments need no special case (plan.md 12).
CREATE TABLE payment_allocations (
    payment_id  uuid   NOT NULL REFERENCES payments (id) ON DELETE CASCADE,
    document_id uuid   NOT NULL REFERENCES documents (id) ON DELETE RESTRICT,
    amount_agorot bigint NOT NULL CHECK (amount_agorot > 0),
    created_at  timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (payment_id, document_id)
);

CREATE INDEX payment_allocations_document_idx ON payment_allocations (document_id);

-- ---------------------------------------------------------- immutability ----

-- A payment that has produced a receipt is as fixed as the receipt itself: the
-- receipt's snapshot states the amount, the method and the date, and those must
-- not drift away from the row they were taken from (plan.md 3.1, 11).
CREATE OR REPLACE FUNCTION payments_enforce_immutability() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF OLD.receipt_document_id IS NOT NULL THEN
            RAISE EXCEPTION 'payment % has receipt % and cannot be deleted',
                OLD.id, OLD.receipt_document_id
                USING ERRCODE = 'restrict_violation';
        END IF;
        RETURN OLD;
    END IF;

    -- Attaching the receipt is the one write allowed on an unreceipted payment
    -- besides ordinary edits; everything else about it must stay as recorded.
    IF OLD.receipt_document_id IS NULL THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'payment % has receipt % and cannot be changed',
        OLD.id, OLD.receipt_document_id
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payments_immutable
    BEFORE UPDATE OR DELETE ON payments
    FOR EACH ROW EXECUTE FUNCTION payments_enforce_immutability();

-- Allocations are part of what the receipt states, so they freeze with it.
CREATE OR REPLACE FUNCTION payment_allocations_enforce_immutability() RETURNS trigger AS $$
DECLARE
    receipt_id uuid;
    owning_payment uuid;
    payment_exists boolean;
BEGIN
    owning_payment := CASE WHEN TG_OP = 'DELETE' THEN OLD.payment_id ELSE NEW.payment_id END;

    SELECT receipt_document_id, true INTO receipt_id, payment_exists
    FROM payments WHERE id = owning_payment;

    -- No payment row means it is being deleted and this is the cascade; the
    -- payment's own trigger has already had its say.
    IF payment_exists IS NULL OR receipt_id IS NULL THEN
        RETURN CASE WHEN TG_OP = 'DELETE' THEN OLD ELSE NEW END;
    END IF;

    RAISE EXCEPTION 'allocations of payment % are fixed by receipt %', owning_payment, receipt_id
        USING ERRCODE = 'restrict_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER payment_allocations_immutable
    BEFORE INSERT OR UPDATE OR DELETE ON payment_allocations
    FOR EACH ROW EXECUTE FUNCTION payment_allocations_enforce_immutability();
