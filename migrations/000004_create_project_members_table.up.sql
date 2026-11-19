CREATE TABLE project_members (
    project_id UUID NOT NULL
        REFERENCES projects(id) ON DELETE CASCADE,

    user_id UUID NOT NULL
        REFERENCES users(id) ON DELETE RESTRICT,

    role TEXT NOT NULL
        CHECK (role IN ('owner', 'admin', 'member')),

    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (project_id, user_id)
);

CREATE UNIQUE INDEX project_members_one_owner_per_project_uidx
    ON project_members (project_id)
    WHERE role = 'owner';

CREATE INDEX project_members_user_id_idx
    ON project_members (user_id);