-- Additional seed data for composition testing
-- Assumes products table already exists from migrations

INSERT INTO products (name, price) VALUES
    ('Composed Widget', 9.99),
    ('Composed Gadget', 19.99);

-- Add additional table that doesn't conflict
CREATE TABLE categories (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL
);

INSERT INTO categories (name) VALUES
    ('Electronics'),
    ('Books');
