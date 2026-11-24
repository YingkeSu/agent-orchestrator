// Package execution maps an external orchestrator's ticket/assignment lifecycle
// onto AO sessions. SuperAgent owns the ticket, authorization and attention
// layer; AO owns the session, worktree and PR/CI facts. This service is the
// bridge's AO half: it makes session creation idempotent per external work item
// and projects AO's derived session state back to the orchestrator.
package execution

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

// Store is the durable mapping surface this service owns.
type Store interface {
	GetExternalSession(ctx context.Context, externalRef string) (domain.ExternalSession, bool, error)
	CreateExternalSession(ctx context.Context, rec domain.ExternalSession) error
	UpdateExternalSessionAssignment(ctx context.Context, externalRef, assignmentRef string, updatedAt time.Time) (int64, error)
	DeleteExternalSession(ctx context.Context, externalRef string) error
	ListExternalSessionsByProject(ctx context.Context, project domain.ProjectID) ([]domain.ExternalSession, error)
}

// Sessions is the AO session lifecycle this service delegates to. It is the
// concrete session service in production and a fake in tests.
type Sessions interface {
	Spawn(ctx context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error)
	Get(ctx context.Context, id domain.SessionID) (domain.Session, error)
	Kill(ctx context.Context, id domain.SessionID) (bool, error)
	RollbackSpawn(ctx context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error)
}

// Service is the external-execution bridge.
type Service struct {
	store    Store
	sessions Sessions
	now      func() time.Time
	ensures  singleflight.Group
	stops    singleflight.Group
}

// New builds the service. store and sessions are required.
func New(store Store, sessions Sessions) *Service {
	return &Service{store: store, sessions: sessions, now: func() time.Time { return time.Now().UTC() }}
}

// EnsureInput asks AO to run one external work item's current assignment.
// ExternalRef is the orchestrator's stable work-item id; AssignmentRef is the
// execution generation within it; RequestFingerprint covers the immutable launch
// parameters, so a changed launch request for the same ref is refused.
type EnsureInput struct {
	ExternalRef        string
	ProjectID          domain.ProjectID
	AssignmentRef      string
	RequestFingerprint string
	Spawn              ports.SpawnConfig
}

// Outcome is one ensured mapping plus whether this call created the session.
type Outcome struct {
	External domain.ExternalSession
	Session  domain.Session
	Created  bool
}

// View combines the durable mapping with AO's derived session read model.
type View struct {
	External domain.ExternalSession
	Session  domain.Session
}

// Ensure returns the AO session for an external work item, creating it exactly
// once. A repeated call with the same fingerprint reuses the session and only
// advances assignment_ref; a different fingerprint is a conflict.
func (s *Service) Ensure(ctx context.Context, in EnsureInput) (Outcome, error) {
	in.ExternalRef = strings.TrimSpace(in.ExternalRef)
	in.AssignmentRef = strings.TrimSpace(in.AssignmentRef)
	in.RequestFingerprint = strings.TrimSpace(in.RequestFingerprint)
	if in.ExternalRef == "" {
		return Outcome{}, apierr.Invalid("EXTERNAL_REF_REQUIRED", "externalRef is required", nil)
	}
	if len(in.ExternalRef) > 256 {
		return Outcome{}, apierr.Invalid("EXTERNAL_REF_TOO_LONG", "externalRef must be 256 characters or fewer", nil)
	}
	if in.RequestFingerprint == "" {
		return Outcome{}, apierr.Invalid("REQUEST_FINGERPRINT_REQUIRED", "requestFingerprint is required", nil)
	}
	if in.ProjectID == "" {
		return Outcome{}, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	if in.Spawn.ProjectID != "" && in.Spawn.ProjectID != in.ProjectID {
		return Outcome{}, apierr.Invalid("PROJECT_MISMATCH", "spawn projectId must match the execution request", nil)
	}
	in.Spawn.ProjectID = in.ProjectID

	// Shared work outlives the first caller's request: a retry or another
	// request that joins the same key must not inherit its cancellation. The
	// timeout bounds a wedged spawn; the fingerprint is part of the key so two
	// different launch requests for one ref cannot share an Outcome and skip
	// the conflict check in reuse().
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Minute)
	defer cancel()
	key := in.ExternalRef + "\x00" + in.RequestFingerprint
	result, err, _ := s.ensures.Do(key, func() (any, error) {
		return s.ensure(work, in)
	})
	if err != nil {
		return Outcome{}, err
	}
	return result.(Outcome), nil
}

func (s *Service) ensure(ctx context.Context, in EnsureInput) (Outcome, error) {
	existing, ok, err := s.store.GetExternalSession(ctx, in.ExternalRef)
	if err != nil {
		return Outcome{}, err
	}
	if ok {
		return s.reuse(ctx, existing, in)
	}

	sess, _, _, err := s.sessions.Spawn(ctx, in.Spawn)
	if err != nil {
		return Outcome{}, err
	}
	now := s.now()
	rec := domain.ExternalSession{
		ExternalRef:        in.ExternalRef,
		ProjectID:          in.ProjectID,
		SessionID:          sess.ID,
		AssignmentRef:      in.AssignmentRef,
		RequestFingerprint: in.RequestFingerprint,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := s.store.CreateExternalSession(ctx, rec); err != nil {
		return s.recoverMappingConflict(ctx, sess.ID, in, err)
	}
	return Outcome{External: rec, Session: sess, Created: true}, nil
}

// reuse returns an existing mapping, advancing its assignment generation. A
// changed launch fingerprint is a conflict rather than a silent reuse.
func (s *Service) reuse(ctx context.Context, existing domain.ExternalSession, in EnsureInput) (Outcome, error) {
	if existing.RequestFingerprint != in.RequestFingerprint || existing.ProjectID != in.ProjectID {
		return Outcome{}, apierr.Conflict("EXTERNAL_REF_CONFLICT",
			"externalRef is already bound to a different launch request",
			map[string]any{"sessionId": string(existing.SessionID), "projectId": string(existing.ProjectID)})
	}
	// Read the session before advancing the mapping: a mapping advance that the
	// caller then cannot observe would leave the stored generation ahead of what
	// the orchestrator was told.
	sess, err := s.sessions.Get(ctx, existing.SessionID)
	if err != nil {
		return Outcome{}, err
	}
	if existing.AssignmentRef != in.AssignmentRef {
		now := s.now()
		rows, err := s.store.UpdateExternalSessionAssignment(ctx, existing.ExternalRef, in.AssignmentRef, now)
		if err != nil {
			return Outcome{}, err
		}
		if rows == 0 {
			return Outcome{}, apierr.NotFound("EXTERNAL_SESSION_NOT_FOUND", "External session mapping disappeared")
		}
		existing.AssignmentRef = in.AssignmentRef
		existing.UpdatedAt = now
	}
	return Outcome{External: existing, Session: sess, Created: false}, nil
}

// recoverMappingConflict rolls back the session this call created when a
// concurrent writer won the unique mapping, then returns the winner. The loser
// of the race must never leave an orphan session behind.
func (s *Service) recoverMappingConflict(ctx context.Context, spawned domain.SessionID, in EnsureInput, cause error) (Outcome, error) {
	// The create may have failed because the request was cancelled; the rollback
	// must still run or the spawned session becomes an orphan.
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if _, rollbackErr := s.sessions.RollbackSpawn(rollbackCtx, spawned); rollbackErr != nil {
		return Outcome{}, fmt.Errorf("record external session mapping: %w; rollback %s: %v", cause, spawned, rollbackErr)
	}
	winner, ok, readErr := s.store.GetExternalSession(ctx, in.ExternalRef)
	if readErr != nil || !ok {
		return Outcome{}, fmt.Errorf("record external session mapping: %w", cause)
	}
	return s.reuse(ctx, winner, in)
}

// Get projects the mapping and AO's derived session state.
func (s *Service) Get(ctx context.Context, externalRef string) (View, error) {
	rec, ok, err := s.store.GetExternalSession(ctx, strings.TrimSpace(externalRef))
	if err != nil {
		return View{}, err
	}
	if !ok {
		return View{}, apierr.NotFound("EXTERNAL_SESSION_NOT_FOUND", "Unknown external session")
	}
	sess, err := s.sessions.Get(ctx, rec.SessionID)
	if err != nil {
		return View{}, err
	}
	return View{External: rec, Session: sess}, nil
}

// List projects every external session of a project, newest first.
func (s *Service) List(ctx context.Context, project domain.ProjectID) ([]View, error) {
	if project == "" {
		return nil, apierr.Invalid("PROJECT_ID_REQUIRED", "projectId is required", nil)
	}
	recs, err := s.store.ListExternalSessionsByProject(ctx, project)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(recs))
	for _, rec := range recs {
		sess, err := s.sessions.Get(ctx, rec.SessionID)
		if err != nil {
			return nil, err
		}
		out = append(out, View{External: rec, Session: sess})
	}
	return out, nil
}

// Stop ends the external work item's AO session. It is idempotent: an already
// terminated session is returned without a second teardown, and the mapping is
// kept so a replaced orchestrator can still read the final state.
func (s *Service) Stop(ctx context.Context, externalRef, reason string) (View, bool, error) {
	ref := strings.TrimSpace(externalRef)
	slog.Info("external execution stop", "externalRef", ref, "reason", strings.TrimSpace(reason))
	type stopOutcome struct {
		view    View
		stopped bool
	}
	result, err, _ := s.stops.Do(ref, func() (any, error) {
		view, err := s.Get(ctx, ref)
		if err != nil {
			return nil, err
		}
		if view.Session.IsTerminated {
			return stopOutcome{view: view}, nil
		}
		if _, err := s.sessions.Kill(ctx, view.External.SessionID); err != nil {
			return nil, err
		}
		sess, err := s.sessions.Get(ctx, view.External.SessionID)
		if err != nil {
			return nil, err
		}
		return stopOutcome{view: View{External: view.External, Session: sess}, stopped: true}, nil
	})
	if err != nil {
		return View{}, false, err
	}
	out := result.(stopOutcome)
	return out.view, out.stopped, nil
}
