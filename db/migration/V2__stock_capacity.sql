-- Returned reservations may exceed the maximum stock accepted by a single replenishment.
ALTER TABLE products ALTER COLUMN stock TYPE BIGINT;
