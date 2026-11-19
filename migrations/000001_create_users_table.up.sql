CREATE TABLE users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    name VARCHAR(100) NOT NULL
        CHECK (name ~ '[^[:space:]]'),

    email TEXT NOT NULL
        CHECK (char_length(email) <= 254),

    password_hash TEXT NOT NULL
        CHECK (password_hash <> ''),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX users_email_lower_uidx
    ON users (lower(email));