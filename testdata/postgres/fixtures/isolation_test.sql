-- Simple schema for testing database isolation
CREATE TABLE counters (
    id SERIAL PRIMARY KEY,
    value INTEGER NOT NULL DEFAULT 0
);

INSERT INTO counters (value) VALUES (100);
