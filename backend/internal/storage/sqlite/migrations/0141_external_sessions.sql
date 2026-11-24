-- Summary: durable mapping from an external orchestrator's work item to one AO session.
--
-- An external orchestrator (for example SuperAgent) owns the ticket/assignment
-- lifecycle; AO owns the execution session, worktree, and PR/CI facts. This table
-- is the only durable link between the two: it makes session creation idempotent
-- across retries and daemon restarts, and it lets a replaced orchestrator find
-- the session it already asked AO to run.
--
-- external_ref is the orchestrator's stable work-item id and is globally unique:
-- orchestrators namespace their refs (for example "superagent:tk_123"). The
-- session_id unique constraint keeps one AO session mapped to at most one
-- external work item. request_fingerprint covers the immutable launch parameters;
-- a different fingerprint for the same ref is a conflict, not a silent reuse.
-- No CDC trigger is added: the row is orchestrator bookkeeping, not a session
-- fact the Kanban reads.
-- +goose Up
-- +goose StatementBegin
CREATE TABLE external_sessions (
    external_ref        TEXT PRIMARY KEY,
    project_id          TEXT NOT NULL REFERENCES projects (id),
    session_id          TEXT NOT NULL UNIQUE REFERENCES sessions (id) ON DELETE CASCADE,
    assignment_ref      TEXT NOT NULL DEFAULT '',
    request_fingerprint TEXT NOT NULL,
    created_at          TIMESTAMP NOT NULL,
    updated_at          TIMESTAMP NOT NULL
);
CREATE INDEX idx_external_sessions_project ON external_sessions (project_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE external_sessions;
-- +goose StatementEnd
