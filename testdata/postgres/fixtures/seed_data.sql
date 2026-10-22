-- Seed data for testing LoadSQLFile
-- This represents a typical SQL dump with schema and data

CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

CREATE TABLE products (
    id SERIAL PRIMARY KEY,
    name TEXT NOT NULL,
    price DECIMAL(10,2) NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT NOW()
);

-- Insert seed data
INSERT INTO users (email, name) VALUES
    ('admin@example.com', 'Admin User'),
    ('user@example.com', 'Test User');

INSERT INTO products (name, price) VALUES
    ('Widget', 9.99),
    ('Gadget', 19.99),
    ('Doohickey', 29.99);
