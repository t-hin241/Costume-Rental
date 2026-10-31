-- Storefront data: catalog categories, images and descriptions, customer
-- profiles and support requests. Files are not stored here: images are
-- addresses of files kept elsewhere.

CREATE TABLE catalog_categories (
    id         text PRIMARY KEY,
    slug       text NOT NULL UNIQUE CHECK (length(slug) <= 120 AND slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    name       text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    image      jsonb,
    position   integer NOT NULL DEFAULT 0,
    updated_at timestamptz NOT NULL
);

ALTER TABLE catalog_items
    ADD COLUMN category_id text REFERENCES catalog_categories (id),
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN images      jsonb NOT NULL DEFAULT '[]';

ALTER TABLE bundles
    ADD COLUMN category_id text REFERENCES catalog_categories (id),
    ADD COLUMN description text NOT NULL DEFAULT '',
    ADD COLUMN images      jsonb NOT NULL DEFAULT '[]';

-- Personal data kept apart from credentials, with one row per user that
-- filled it in.
CREATE TABLE customer_profiles (
    user_id    text PRIMARY KEY REFERENCES users (id),
    full_name  text NOT NULL CHECK (length(full_name) <= 120),
    phone      text NOT NULL CHECK (length(phone) <= 20),
    updated_at timestamptz NOT NULL
);

CREATE TABLE support_requests (
    id           text PRIMARY KEY,
    code         text NOT NULL UNIQUE CHECK (code <> ''),
    customer_id  text NOT NULL REFERENCES users (id),
    topic        text NOT NULL CHECK (topic IN ('BOOKING', 'PAYMENT', 'PRODUCT', 'DELIVERY', 'OTHER')),
    subject      text NOT NULL CHECK (length(subject) BETWEEN 1 AND 160),
    status       text NOT NULL CHECK (status IN ('OPEN', 'IN_PROGRESS', 'RESOLVED')),
    booking_code text,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL
);
CREATE INDEX support_requests_by_customer ON support_requests (customer_id, created_at DESC);
CREATE INDEX support_requests_open ON support_requests (status, created_at DESC);

CREATE TABLE support_messages (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    request_id text NOT NULL REFERENCES support_requests (id),
    sender     text NOT NULL CHECK (sender IN ('CUSTOMER', 'STAFF')),
    author_id  text NOT NULL REFERENCES users (id),
    body       text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    at         timestamptz NOT NULL
);
CREATE INDEX support_messages_by_request ON support_messages (request_id, id);

-- Booking lists sort newest first and the operations views scan by due time.
CREATE INDEX bookings_by_created ON bookings (created_at DESC);
CREATE INDEX bookings_by_status_due ON bookings (status, due_at);
