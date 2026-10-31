-- Local dates the store closes every year, such as solar public holidays.
-- Lunar holidays fall on a different solar date each year and stay in
-- store_closures. 2024 is a leap year, so 02-29 passes and 02-30 does not.
CREATE TABLE store_recurring_closures (
    month  smallint NOT NULL,
    day    smallint NOT NULL,
    reason text NOT NULL DEFAULT '',
    PRIMARY KEY (month, day),
    CHECK (make_date(2024, month, day) IS NOT NULL)
);
