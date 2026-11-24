package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	sessionsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/session"
)

type fakeStore struct {
	mu               sync.Mutex
	mappings         map[string]domain.ExternalSession
	createErr        error
	hideBeforeCreate bool
	createAttempted  bool
	updates          []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{mappings: map[string]domain.ExternalSession{}}
}

func (s *fakeStore) GetExternalSession(_ context.Context, ref string) (domain.ExternalSession, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.mappings[ref]
	if s.hideBeforeCreate && !s.createAttempted {
		return domain.ExternalSession{}, false, nil
	}
	return rec, ok, nil
}

func (s *fakeStore) CreateExternalSession(_ context.Context, rec domain.ExternalSession) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createAttempted = true
	if s.createErr != nil {
		return s.createErr
	}
	s.mappings[rec.ExternalRef] = rec
	return nil
}

func (s *fakeStore) UpdateExternalSessionAssignment(_ context.Context, ref, assignment string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.mappings[ref]
	rec.AssignmentRef = assignment
	rec.UpdatedAt = updatedAt
	s.mappings[ref] = rec
	s.updates = append(s.updates, ref+"="+assignment)
	return nil
}

func (s *fakeStore) DeleteExternalSession(_ context.Context, ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.mappings, ref)
	return nil
}

func (s *fakeStore) ListExternalSessionsByProject(_ context.Context, project domain.ProjectID) ([]domain.ExternalSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ExternalSession
	for _, rec := range s.mappings {
		if rec.ProjectID == project {
			out = append(out, rec)
		}
	}
	return out, nil
}

type fakeSessions struct {
	mu        sync.Mutex
	spawned   []ports.SpawnConfig
	killed    []domain.SessionID
	rollbacks []domain.SessionID
	sessions  map[domain.SessionID]domain.Session
	next      int
	spawnErr  error
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{sessions: map[domain.SessionID]domain.Session{}}
}

func (f *fakeSessions) Spawn(_ context.Context, cfg ports.SpawnConfig) (domain.Session, int, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.spawnErr != nil {
		return domain.Session{}, 0, 0, f.spawnErr
	}
	f.next++
	id := domain.SessionID(fmt.Sprintf("proj-%d", f.next))
	sess := domain.Session{
		SessionRecord: domain.SessionRecord{ID: id, ProjectID: cfg.ProjectID, UpdatedAt: time.Now().UTC()},
		Status:        domain.StatusWorking,
	}
	f.spawned = append(f.spawned, cfg)
	f.sessions[id] = sess
	return sess, 0, 0, nil
}

func (f *fakeSessions) Get(_ context.Context, id domain.SessionID) (domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sess, ok := f.sessions[id]
	if !ok {
		return domain.Session{}, apierr.NotFound("SESSION_NOT_FOUND", "Unknown session")
	}
	return sess, nil
}

func (f *fakeSessions) Kill(_ context.Context, id domain.SessionID) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.killed = append(f.killed, id)
	sess := f.sessions[id]
	sess.IsTerminated = true
	f.sessions[id] = sess
	return true, nil
}

func (f *fakeSessions) RollbackSpawn(_ context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rollbacks = append(f.rollbacks, id)
	delete(f.sessions, id)
	return sessionsvc.RollbackOutcome{Deleted: true}, nil
}

func spawnConfig(project domain.ProjectID) ports.SpawnConfig {
	return ports.SpawnConfig{ProjectID: project, Harness: "claude-code", Prompt: "do it"}
}

func TestEnsureCreatesOnceAndAdvancesAssignment(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)

	first, err := svc.Ensure(context.Background(), EnsureInput{
		ExternalRef: "superagent:tk_1", ProjectID: "proj", AssignmentRef: "asg_1",
		RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	})
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if !first.Created || len(sessions.spawned) != 1 {
		t.Fatalf("first ensure = %+v, spawned=%d; want created once", first, len(sessions.spawned))
	}

	second, err := svc.Ensure(context.Background(), EnsureInput{
		ExternalRef: "superagent:tk_1", ProjectID: "proj", AssignmentRef: "asg_2",
		RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	})
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if second.Created || len(sessions.spawned) != 1 {
		t.Fatalf("second ensure = %+v, spawned=%d; want reuse", second, len(sessions.spawned))
	}
	if second.External.AssignmentRef != "asg_2" {
		t.Fatalf("assignment = %q, want asg_2", second.External.AssignmentRef)
	}
}

func TestEnsureRejectsChangedFingerprint(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	if _, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp1", Spawn: spawnConfig("proj"),
	}); err != nil {
		t.Fatalf("seed ensure: %v", err)
	}
	_, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp2", Spawn: spawnConfig("proj"),
	})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindConflict {
		t.Fatalf("changed fingerprint error = %v, want conflict", err)
	}
	if len(sessions.spawned) != 1 {
		t.Fatalf("spawned=%d, want no second spawn", len(sessions.spawned))
	}
}

func TestEnsureRollsBackLostMappingRace(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	store.mappings["ref"] = domain.ExternalSession{
		ExternalRef: "ref", ProjectID: "proj", SessionID: "proj-0",
		RequestFingerprint: "fp", AssignmentRef: "winner",
	}
	sessions.sessions["proj-0"] = domain.Session{SessionRecord: domain.SessionRecord{ID: "proj-0", ProjectID: "proj"}}
	store.createErr = errors.New("UNIQUE constraint failed")
	store.hideBeforeCreate = true
	svc := New(store, sessions)

	got, err := svc.Ensure(context.Background(), EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(sessions.rollbacks) != 1 || sessions.rollbacks[0] != "proj-1" {
		t.Fatalf("rollbacks = %v, want the spawned loser proj-1", sessions.rollbacks)
	}
	if got.External.SessionID != "proj-0" || got.Created {
		t.Fatalf("winner = %+v, want existing proj-0", got)
	}
}

func TestEnsureConcurrentSameRefCreatesOneSession(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.Ensure(context.Background(), EnsureInput{
				ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
			}); err != nil {
				t.Errorf("ensure: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(sessions.spawned) != 1 {
		t.Fatalf("spawned=%d, want exactly one", len(sessions.spawned))
	}
}

func TestEnsureValidation(t *testing.T) {
	svc := New(newFakeStore(), newFakeSessions())
	for _, tc := range []struct {
		name string
		in   EnsureInput
		code string
	}{
		{"missing ref", EnsureInput{ProjectID: "p", RequestFingerprint: "fp"}, "EXTERNAL_REF_REQUIRED"},
		{"missing fingerprint", EnsureInput{ExternalRef: "r", ProjectID: "p"}, "REQUEST_FINGERPRINT_REQUIRED"},
		{"missing project", EnsureInput{ExternalRef: "r", RequestFingerprint: "fp"}, "PROJECT_ID_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Ensure(context.Background(), tc.in)
			var apiErr *apierr.Error
			if !errors.As(err, &apiErr) || apiErr.Code != tc.code {
				t.Fatalf("error = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestStopIsIdempotent(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	out, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if _, killed, err := svc.Stop(ctx, "ref", "superseded"); err != nil || !killed {
		t.Fatalf("first stop killed=%v err=%v", killed, err)
	}
	if _, killed, err := svc.Stop(ctx, "ref", "superseded"); err != nil || killed {
		t.Fatalf("second stop killed=%v err=%v, want idempotent no-op", killed, err)
	}
	if len(sessions.killed) != 1 || sessions.killed[0] != out.External.SessionID {
		t.Fatalf("killed = %v, want one kill of %s", sessions.killed, out.External.SessionID)
	}
}

func TestGetUnknownRef(t *testing.T) {
	svc := New(newFakeStore(), newFakeSessions())
	_, err := svc.Get(context.Background(), "missing")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindNotFound {
		t.Fatalf("error = %v, want not found", err)
	}
}

func TestListProjectsExternalSessions(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	for _, ref := range []string{"a", "b"} {
		if _, err := svc.Ensure(ctx, EnsureInput{
			ExternalRef: ref, ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
		}); err != nil {
			t.Fatalf("ensure %s: %v", ref, err)
		}
	}
	if _, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "c", ProjectID: "other", RequestFingerprint: "fp", Spawn: spawnConfig("other"),
	}); err != nil {
		t.Fatalf("ensure other: %v", err)
	}
	got, err := svc.List(ctx, "proj")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("listed %d, want 2", len(got))
	}
}
