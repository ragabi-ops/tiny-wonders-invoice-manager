-- 0003_customers_catalog_activities.sql
-- Phase 2: customers, the reusable service catalogue, activities, and the
-- indexes that make search fast (plan.md 8).
--
-- Duplicate customers are warned about, never blocked: two people really can
-- share a phone number, and a hard constraint would make the operator fight the
-- system instead of the data.

-- Trigram indexes give substring search on Hebrew names, which PostgreSQL's
-- word-based full-text search does not stem usefully.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- -------------------------------------------------------------- customers --

CREATE TABLE customers (
    id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_type          text        NOT NULL DEFAULT 'PERSON'
                                       CHECK (customer_type IN ('PERSON', 'BUSINESS')),
    display_name           text        NOT NULL CHECK (length(btrim(display_name)) > 0),
    legal_name             text        NOT NULL DEFAULT '',
    -- Company number or national ID. Optional: not every customer needs one
    -- (plan.md 8).
    business_or_id_number  text        NOT NULL DEFAULT '',
    phone                  text        NOT NULL DEFAULT '',
    email                  text        NOT NULL DEFAULT '',
    address                text        NOT NULL DEFAULT '',
    notes                  text        NOT NULL DEFAULT '',
    preferred_delivery     text        NOT NULL DEFAULT 'NONE'
                                       CHECK (preferred_delivery IN ('EMAIL', 'WHATSAPP', 'NONE')),
    active                 boolean     NOT NULL DEFAULT true,
    created_at             timestamptz NOT NULL DEFAULT now(),
    updated_at             timestamptz NOT NULL DEFAULT now(),
    created_by             uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_by             uuid        REFERENCES users (id) ON DELETE SET NULL,

    -- Everything one might search a customer by, in one indexable column. The
    -- phone appears twice — as typed and as bare digits — so "050-123-4567"
    -- is found by either spelling. A generated column cannot reference another
    -- generated column, hence the repeated expression.
    search_text text GENERATED ALWAYS AS (
        display_name || ' ' || legal_name || ' ' || business_or_id_number || ' ' ||
        phone || ' ' || regexp_replace(phone, '[^0-9]', '', 'g') || ' ' || email
    ) STORED,

    -- Digits only, so "050-123-4567" and "0501234567" match each other when
    -- looking for duplicates.
    phone_digits text GENERATED ALWAYS AS (
        regexp_replace(phone, '[^0-9]', '', 'g')
    ) STORED
);

CREATE TRIGGER customers_set_updated_at
    BEFORE UPDATE ON customers
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX customers_search_trgm_idx ON customers USING gin (search_text gin_trgm_ops);
CREATE INDEX customers_display_name_idx ON customers (lower(display_name));
CREATE INDEX customers_active_idx ON customers (active, display_name);

-- Duplicate lookups. Partial, because blank is the common case and indexing it
-- would be one huge useless bucket.
CREATE INDEX customers_phone_digits_idx ON customers (phone_digits) WHERE phone_digits <> '';
CREATE INDEX customers_email_lower_idx ON customers (lower(email)) WHERE email <> '';
CREATE INDEX customers_business_number_idx ON customers (business_or_id_number)
    WHERE business_or_id_number <> '';

-- --------------------------------------------------------------- services --

-- Reusable catalogue items. Documents may also carry free-form lines, so this
-- is a convenience, not a required source for every line (plan.md 8).
CREATE TABLE services (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text        NOT NULL CHECK (length(btrim(name)) > 0),
    description          text        NOT NULL DEFAULT '',
    -- Money is integer agorot everywhere (plan.md 3.6).
    default_price_agorot bigint      NOT NULL DEFAULT 0 CHECK (default_price_agorot >= 0),
    unit                 text        NOT NULL DEFAULT '',
    category             text        NOT NULL DEFAULT '',
    active               boolean     NOT NULL DEFAULT true,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    created_by           uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_by           uuid        REFERENCES users (id) ON DELETE SET NULL,

    search_text text GENERATED ALWAYS AS (
        name || ' ' || description || ' ' || category
    ) STORED
);

CREATE TRIGGER services_set_updated_at
    BEFORE UPDATE ON services
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX services_search_trgm_idx ON services USING gin (search_text gin_trgm_ops);
CREATE INDEX services_active_idx ON services (active, name);
CREATE INDEX services_category_idx ON services (category) WHERE category <> '';

-- ------------------------------------------------------------- activities --

-- An optional operational dimension: documents, payments and expenses may
-- reference an activity so an event's profitability can be reported. This is
-- not booking or ticketing (plan.md 8, 21).
CREATE TABLE activities (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL CHECK (length(btrim(name)) > 0),
    start_at    timestamptz,
    end_at      timestamptz,
    location    text        NOT NULL DEFAULT '',
    status      text        NOT NULL DEFAULT 'PLANNED'
                            CHECK (status IN ('PLANNED', 'ACTIVE', 'DONE', 'CANCELLED')),
    notes       text        NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    created_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_by  uuid        REFERENCES users (id) ON DELETE SET NULL,

    -- An activity that ends before it starts is a data-entry error, not a state
    -- the rest of the system should have to tolerate.
    CONSTRAINT activities_period_ordered
        CHECK (start_at IS NULL OR end_at IS NULL OR end_at >= start_at),

    search_text text GENERATED ALWAYS AS (
        name || ' ' || location || ' ' || notes
    ) STORED
);

CREATE TRIGGER activities_set_updated_at
    BEFORE UPDATE ON activities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX activities_search_trgm_idx ON activities USING gin (search_text gin_trgm_ops);
CREATE INDEX activities_status_idx ON activities (status, start_at DESC NULLS LAST);
CREATE INDEX activities_start_at_idx ON activities (start_at DESC NULLS LAST);
