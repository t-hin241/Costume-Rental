-- Decision D38: an offering may belong to several categories. The first one
-- (position 0) is the one shown on its card. Categories only present an
-- offering, so bookings are not involved.

CREATE TABLE catalog_item_categories (
    item_id     text NOT NULL REFERENCES catalog_items (id),
    category_id text NOT NULL REFERENCES catalog_categories (id),
    position    integer NOT NULL,
    PRIMARY KEY (item_id, category_id),
    UNIQUE (item_id, position)
);
CREATE INDEX catalog_item_categories_by_category ON catalog_item_categories (category_id);

CREATE TABLE bundle_categories (
    bundle_id   text NOT NULL REFERENCES bundles (id),
    category_id text NOT NULL REFERENCES catalog_categories (id),
    position    integer NOT NULL,
    PRIMARY KEY (bundle_id, category_id),
    UNIQUE (bundle_id, position)
);
CREATE INDEX bundle_categories_by_category ON bundle_categories (category_id);

INSERT INTO catalog_item_categories (item_id, category_id, position)
SELECT id, category_id, 0 FROM catalog_items WHERE category_id IS NOT NULL;

INSERT INTO bundle_categories (bundle_id, category_id, position)
SELECT id, category_id, 0 FROM bundles WHERE category_id IS NOT NULL;

ALTER TABLE catalog_items DROP COLUMN category_id;
ALTER TABLE bundles DROP COLUMN category_id;
