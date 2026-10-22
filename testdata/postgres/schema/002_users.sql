CREATE TABLE users (
    id SERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    name TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
