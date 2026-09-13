-- 0002_regulatory_seed.sql
-- Seeds only values that plan.md states explicitly. Anything the compliance
-- gate has not resolved is seeded as disabled/unknown rather than guessed
-- (plan.md 2, 22.3).

-- Business mode. EXEMPT_DEALER forbids VAT and tax invoices entirely.
INSERT INTO regulatory_config (key, effective_from, value, note) VALUES
    ('business_mode', DATE '2000-01-01', '"EXEMPT_DEALER"'::jsonb,
     'Initial mode per plan.md 2. AUTHORIZED_DEALER is architected for but disabled in v1.');

-- Exempt-dealer annual turnover threshold, in agorot.
-- 2026: 122,833 ILS = 12,283,300 agorot (plan.md 2).
-- Earlier/later years are intentionally absent: do not invent regulatory values.
INSERT INTO regulatory_config (key, effective_from, value, note) VALUES
    ('exempt_turnover_threshold', DATE '2026-01-01',
     '{"amount_agorot": 12283300, "currency": "ILS"}'::jsonb,
     'Published 2026 exempt-dealer threshold, 122,833 ILS. Re-check yearly against gov.il before relying on it.');

-- Document types this deployment may issue. RECEIPT and TRANSACTION_INVOICE
-- only; TAX_INVOICE is absent by design in exempt-dealer mode.
INSERT INTO regulatory_config (key, effective_from, value, note) VALUES
    ('allowed_document_types', DATE '2000-01-01',
     '["TRANSACTION_INVOICE", "PAYMENT_REQUEST", "RECEIPT"]'::jsonb,
     'Exempt dealer must never issue a tax invoice (חשבונית מס).');

-- Compliance gate. Real issuance stays off until a tax adviser signs off every
-- item in plan.md 2. The value records who cleared what, and when.
INSERT INTO regulatory_config (key, effective_from, value, note) VALUES
    ('compliance_gate', DATE '2000-01-01',
     '{
        "real_issuance_enabled": false,
        "cleared_by": null,
        "cleared_at": null,
        "items": {
          "required_fields_and_wording": "UNRESOLVED",
          "receipt_timing_and_payment_details": "UNRESOLVED",
          "cancellation_and_correction": "UNRESOLVED",
          "computerized_document_and_signature": "UNRESOLVED",
          "legal_retention": "UNRESOLVED",
          "software_registration_required": "UNRESOLVED",
          "accountant_export_requirements": "UNRESOLVED"
        }
      }'::jsonb,
     'plan.md 2 production compliance gate. Every item must be resolved by a tax adviser before real_issuance_enabled may be set true.');

-- Computerized-document/signature behavior is unresolved, so the adapter stays
-- disabled rather than implementing a guessed signing scheme.
INSERT INTO regulatory_config (key, effective_from, value, note) VALUES
    ('computerized_document_settings', DATE '2000-01-01',
     '{"enabled": false, "signature_mode": "NONE"}'::jsonb,
     'Enabled only after the compliance gate resolves signature requirements.');
