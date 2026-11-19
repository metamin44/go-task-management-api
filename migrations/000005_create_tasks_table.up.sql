CREATE TABLE tasks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    project_id UUID NOT NULL
        REFERENCES projects(id) ON DELETE CASCADE,

    created_by UUID NOT NULL
        REFERENCES users(id) ON DELETE RESTRICT,

    assigned_to UUID NULL,

    title VARCHAR(200) NOT NULL
        CHECK (title ~ '[^[:space:]]'),

    description TEXT NULL,

    status TEXT NOT NULL DEFAULT 'todo'
        CHECK (status IN ('todo', 'in_progress', 'done')),

    priority TEXT NOT NULL DEFAULT 'medium'
        CHECK (priority IN ('low', 'medium', 'high', 'urgent')),

    due_at TIMESTAMPTZ NULL,

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT tasks_project_assignee_fkey
        FOREIGN KEY (project_id, assigned_to)
        REFERENCES project_members (project_id, user_id)
        MATCH SIMPLE
        ON DELETE NO ACTION
);

CREATE INDEX tasks_project_assignee_idx
    ON tasks (project_id, assigned_to);

CREATE INDEX tasks_assignee_idx
    ON tasks (assigned_to)
    WHERE assigned_to IS NOT NULL;

CREATE INDEX tasks_creator_idx
    ON tasks (created_by);