-- The earliest pickup a booking ever had. Cancellation notice counts from it,
-- so moving the date cannot lower the fee (decision D34). Bookings already made
-- start from their current pickup: the rule does not raise a fee retroactively.

ALTER TABLE bookings ADD COLUMN earliest_pickup_at timestamptz;
UPDATE bookings SET earliest_pickup_at = pickup_at;
ALTER TABLE bookings ALTER COLUMN earliest_pickup_at SET NOT NULL;
ALTER TABLE bookings ADD CONSTRAINT bookings_earliest_pickup CHECK (earliest_pickup_at <= pickup_at);
