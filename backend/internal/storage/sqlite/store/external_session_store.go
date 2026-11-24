package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/storage/sqlite/gen"
)

// GetExternalSession returns the mapping for an external work item, or ok=false
// when no orchestrator has claimed one.
func (s *Store) GetExternalSession(ctx context.Context, externalRef string) (domain.ExternalSession, bool, error) {
	row, err := s.qr.GetExternalSession(ctx, externalRef)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ExternalSession{}, false, nil
	}
	if err != nil {
		return domain.ExternalSession{}, false, fmt.Errorf("get external session %q: %w", externalRef, err)
	}
	return externalSessionFromRow(row), true, nil
}

// CreateExternalSession records the mapping. The session_id unique constraint
// keeps one AO session bound to one external work item; a duplicate returns the
// database error unchanged so the caller can re-read the winner.
func (s *Store) CreateExternalSession(ctx context.Context, rec domain.ExternalSession) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.CreateExternalSession(ctx, gen.CreateExternalSessionParams{
		ExternalRef:        rec.ExternalRef,
		ProjectID:          rec.ProjectID,
		SessionID:          rec.SessionID,
		AssignmentRef:      rec.AssignmentRef,
		RequestFingerprint: rec.RequestFingerprint,
		CreatedAt:          rec.CreatedAt,
		UpdatedAt:          rec.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("create external session %q: %w", rec.ExternalRef, err)
	}
	return nil
}

// UpdateExternalSessionAssignment advances the current assignment generation
// without changing the session or its immutable launch fingerprint.
func (s *Store) UpdateExternalSessionAssignment(ctx context.Context, externalRef, assignmentRef string, updatedAt time.Time) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	rows, err := s.qw.UpdateExternalSessionAssignment(ctx, gen.UpdateExternalSessionAssignmentParams{
		AssignmentRef: assignmentRef,
		UpdatedAt:     updatedAt,
		ExternalRef:   externalRef,
	})
	if err != nil {
		return 0, fmt.Errorf("update external session %q: %w", externalRef, err)
	}
	return rows, nil
}

// DeleteExternalSession removes a mapping. It never touches the AO session.
func (s *Store) DeleteExternalSession(ctx context.Context, externalRef string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.qw.DeleteExternalSession(ctx, externalRef); err != nil {
		return fmt.Errorf("delete external session %q: %w", externalRef, err)
	}
	return nil
}

// ListExternalSessionsByProject returns the project's mappings, newest first.
func (s *Store) ListExternalSessionsByProject(ctx context.Context, project domain.ProjectID) ([]domain.ExternalSession, error) {
	rows, err := s.qr.ListExternalSessionsByProject(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list external sessions for %s: %w", project, err)
	}
	out := make([]domain.ExternalSession, 0, len(rows))
	for _, row := range rows {
		out = append(out, externalSessionFromRow(row))
	}
	return out, nil
}

func externalSessionFromRow(row gen.ExternalSession) domain.ExternalSession {
	return domain.ExternalSession{
		ExternalRef:        row.ExternalRef,
		ProjectID:          row.ProjectID,
		SessionID:          row.SessionID,
		AssignmentRef:      row.AssignmentRef,
		RequestFingerprint: row.RequestFingerprint,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
	}
}
