-- Catalog: packages, items with variants (SKUs), bundles over shared
-- inventory, and package prices. Entries are archived, never deleted, so
-- history stays linkable; bookings keep their own snapshot anyway.

CREATE TABLE rental_packages (
    id             text PRIMARY KEY,
    code           text NOT NULL UNIQUE CHECK (code <> ''),
    label          text NOT NULL CHECK (label <> ''),
    duration_hours integer NOT NULL CHECK (duration_hours > 0),
    status         text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    updated_at     timestamptz NOT NULL
);

CREATE TABLE catalog_items (
    id                    text PRIMARY KEY,
    slug                  text NOT NULL UNIQUE CHECK (length(slug) <= 120 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name                  text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status                text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    individually_rentable boolean NOT NULL,
    currency              text NOT NULL CHECK (currency = 'VND'),
    security_deposit      bigint NOT NULL CHECK (security_deposit >= 0),
    -- Late-fee base per day (fee policy §2.1).
    daily_rate            bigint NOT NULL CHECK (daily_rate > 0),
    buffer_class          text NOT NULL CHECK (buffer_class <> ''),
    version               integer NOT NULL CHECK (version >= 1),
    updated_at            timestamptz NOT NULL
);

-- A variant is a non-interchangeable SKU and its own inventory source.
CREATE TABLE item_variants (
    id       text PRIMARY KEY,
    item_id  text NOT NULL REFERENCES catalog_items (id),
    sku      text NOT NULL UNIQUE CHECK (sku <> ''),
    size     text NOT NULL DEFAULT '',
    color    text NOT NULL DEFAULT '',
    status   text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    position integer NOT NULL,
    UNIQUE (item_id, id)
);

CREATE TABLE item_prices (
    item_id    text NOT NULL REFERENCES catalog_items (id),
    package_id text NOT NULL REFERENCES rental_packages (id),
    amount     bigint NOT NULL CHECK (amount > 0),
    PRIMARY KEY (item_id, package_id)
);

CREATE TABLE bundles (
    id               text PRIMARY KEY,
    slug             text NOT NULL UNIQUE CHECK (length(slug) <= 120 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name             text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    status           text NOT NULL CHECK (status IN ('DRAFT', 'ACTIVE', 'ARCHIVED')),
    currency         text NOT NULL CHECK (currency = 'VND'),
    -- When set, replaces the sum of component deposits for one bundle.
    deposit_override bigint CHECK (deposit_override >= 0),
    version          integer NOT NULL CHECK (version >= 1),
    updated_at       timestamptz NOT NULL
);

CREATE TABLE bundle_components (
    bundle_id text NOT NULL REFERENCES bundles (id),
    key       text NOT NULL CHECK (key <> ''),
    item_id   text NOT NULL REFERENCES catalog_items (id),
    quantity  integer NOT NULL CHECK (quantity >= 1),
    position  integer NOT NULL,
    PRIMARY KEY (bundle_id, key),
    UNIQUE (bundle_id, key, item_id)
);

-- The variants a customer may pick for a component. The composite keys make
-- the database reject an option that belongs to a different item.
CREATE TABLE bundle_component_options (
    bundle_id     text NOT NULL,
    component_key text NOT NULL,
    item_id       text NOT NULL,
    variant_id    text NOT NULL,
    position      integer NOT NULL,
    PRIMARY KEY (bundle_id, component_key, variant_id),
    FOREIGN KEY (bundle_id, component_key, item_id)
        REFERENCES bundle_components (bundle_id, key, item_id) ON DELETE CASCADE,
    FOREIGN KEY (item_id, variant_id) REFERENCES item_variants (item_id, id)
);

CREATE TABLE bundle_prices (
    bundle_id  text NOT NULL REFERENCES bundles (id),
    package_id text NOT NULL REFERENCES rental_packages (id),
    amount     bigint NOT NULL CHECK (amount > 0),
    PRIMARY KEY (bundle_id, package_id)
);
