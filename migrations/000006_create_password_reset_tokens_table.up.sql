CREATE TABLE password_reset_tokens (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    token_hash BYTEA NOT NULL UNIQUE
        CHECK (octet_length(token_hash) = 32),

    expires_at TIMESTAMPTZ NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    consumed_at TIMESTAMPTZ NULL,

    CONSTRAINT password_reset_tokens_expires_after_created
        CHECK (expires_at > created_at)
);

CREATE INDEX password_reset_tokens_user_id_idx
    ON password_reset_tokens (user_id);

CREATE INDEX password_reset_tokens_expires_at_idx
    ON password_reset_tokens (expires_at);