-- name: GetExternalSession :one
SELECT external_ref, project_id, session_id, assignment_ref, request_fingerprint, created_at, updated_at
FROM external_sessions
WHERE external_ref = ?;

-- name: CreateExternalSession :exec
INSERT INTO external_sessions (
    external_ref, project_id, session_id, assignment_ref, request_fingerprint, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: UpdateExternalSessionAssignment :exec
UPDATE external_sessions
SET assignment_ref = ?, updated_at = ?
WHERE external_ref = ?;

-- name: DeleteExternalSession :exec
DELETE FROM external_sessions WHERE external_ref = ?;

-- name: ListExternalSessionsByProject :many
SELECT external_ref, project_id, session_id, assignment_ref, request_fingerprint, created_at, updated_at
FROM external_sessions
WHERE project_id = ?
ORDER BY created_at DESC, external_ref;
