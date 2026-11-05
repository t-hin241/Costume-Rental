-- Keeps the category shown on the card; the others cannot fit in one column.
ALTER TABLE catalog_items ADD COLUMN category_id text REFERENCES catalog_categories (id);
ALTER TABLE bundles ADD COLUMN category_id text REFERENCES catalog_categories (id);

UPDATE catalog_items i SET category_id = c.category_id
FROM catalog_item_categories c WHERE c.item_id = i.id AND c.position = 0;

UPDATE bundles b SET category_id = c.category_id
FROM bundle_categories c WHERE c.bundle_id = b.id AND c.position = 0;

DROP TABLE bundle_categories;
DROP TABLE catalog_item_categories;
