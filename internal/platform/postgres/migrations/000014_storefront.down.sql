DROP INDEX bookings_by_status_due;
DROP INDEX bookings_by_created;
DROP TABLE support_messages;
DROP TABLE support_requests;
DROP TABLE customer_profiles;
ALTER TABLE bundles DROP COLUMN images, DROP COLUMN description, DROP COLUMN category_id;
ALTER TABLE catalog_items DROP COLUMN images, DROP COLUMN description, DROP COLUMN category_id;
DROP TABLE catalog_categories;
