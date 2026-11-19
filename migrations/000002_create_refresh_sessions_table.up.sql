CREATE TABLE refresh_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    session_id UUID NOT NULL,

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE CASCADE,

    token_hash BYTEA NOT NULL UNIQUE
        CHECK (octet_length(token_hash) = 32),

    expires_at TIMESTAMPTZ NOT NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    revoked_at TIMESTAMPTZ NULL,

    replaced_by UUID NULL,

    CONSTRAINT refresh_sessions_expires_after_created
        CHECK (expires_at > created_at),

    CONSTRAINT refresh_sessions_replaced_requires_revocation
        CHECK (replaced_by IS NULL OR revoked_at IS NOT NULL),

    CONSTRAINT refresh_sessions_no_self_replacement
        CHECK (replaced_by IS NULL OR replaced_by <> id),

    CONSTRAINT refresh_sessions_replaced_by_fkey
        FOREIGN KEY (replaced_by)
        REFERENCES refresh_sessions(id)
        ON DELETE SET NULL
        DEFERRABLE INITIALLY DEFERRED
);

CREATE UNIQUE INDEX refresh_sessions_one_active_per_session_uidx
    ON refresh_sessions (session_id)
    WHERE revoked_at IS NULL;

CREATE INDEX refresh_sessions_user_id_idx
    ON refresh_sessions (user_id);

CREATE INDEX refresh_sessions_session_id_idx
    ON refresh_sessions (session_id);

CREATE INDEX refresh_sessions_revoked_at_idx
    ON refresh_sessions (revoked_at)
    WHERE revoked_at IS NOT NULL;

CREATE INDEX refresh_sessions_expires_at_idx
    ON refresh_sessions (expires_at);