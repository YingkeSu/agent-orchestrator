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
	unique           bool
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
	if s.unique {
		if _, ok := s.mappings[rec.ExternalRef]; ok {
			return errors.New("UNIQUE constraint failed: external_sessions.external_ref")
		}
	}
	s.mappings[rec.ExternalRef] = rec
	return nil
}

func (s *fakeStore) UpdateExternalSessionAssignment(_ context.Context, ref, assignment string, updatedAt time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.mappings[ref]
	if !ok {
		return 0, nil
	}
	rec.AssignmentRef = assignment
	rec.UpdatedAt = updatedAt
	s.mappings[ref] = rec
	s.updates = append(s.updates, ref+"="+assignment)
	return 1, nil
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
	rollbacks         []domain.SessionID
	rollbackCanceled  bool
	sessions          map[domain.SessionID]domain.Session
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

func (f *fakeSessions) List(_ context.Context, filter sessionsvc.ListFilter) ([]domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Session, 0, len(f.sessions))
	for _, sess := range f.sessions {
		if filter.ProjectID == "" || sess.ProjectID == filter.ProjectID {
			out = append(out, sess)
		}
	}
	return out, nil
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

func (f *fakeSessions) RollbackSpawn(ctx context.Context, id domain.SessionID) (sessionsvc.RollbackOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctx.Err() != nil {
		f.rollbackCanceled = true
	}
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


func TestEnsureRejectsProjectMismatch(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	if _, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	}); err != nil {
		t.Fatalf("seed ensure: %v", err)
	}
	_, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "other", RequestFingerprint: "fp", Spawn: spawnConfig("other"),
	})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.Kind != apierr.KindConflict {
		t.Fatalf("project mismatch error = %v, want conflict", err)
	}
}

func TestEnsureConcurrentFingerprintsCreateOneSession(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	store.unique = true
	svc := New(store, sessions)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var conflicts int
	for _, fp := range []string{"fp1", "fp2"} {
		wg.Add(1)
		go func(fp string) {
			defer wg.Done()
			_, err := svc.Ensure(context.Background(), EnsureInput{
				ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: fp, Spawn: spawnConfig("proj"),
			})
			var apiErr *apierr.Error
			if errors.As(err, &apiErr) && apiErr.Kind == apierr.KindConflict {
				mu.Lock()
				conflicts++
				mu.Unlock()
			}
		}(fp)
	}
	wg.Wait()
	if len(store.mappings) != 1 {
		t.Fatalf("mappings=%d, want exactly one", len(store.mappings))
	}
	if conflicts != 1 {
		t.Fatalf("conflicts=%d, want exactly one", conflicts)
	}
	// The racing caller either lost at CreateExternalSession (rollback) or saw
	// the winner before spawning; both are correct as long as no orphan remains.
	if len(sessions.spawned)-len(sessions.rollbacks) != 1 {
		t.Fatalf("spawned=%d rollbacks=%d, want exactly one surviving session", len(sessions.spawned), len(sessions.rollbacks))
	}
}

func TestStopConcurrentRunsOneTeardown(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	if _, err := svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := svc.Stop(ctx, "ref", "superseded"); err != nil {
				t.Errorf("stop: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(sessions.killed) != 1 {
		t.Fatalf("killed=%d, want one teardown", len(sessions.killed))
	}
}

func TestRecoverMappingRollsBackWithLiveContext(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	store.createErr = errors.New("UNIQUE constraint failed")
	store.hideBeforeCreate = true
	svc := New(store, sessions)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = svc.Ensure(ctx, EnsureInput{
		ExternalRef: "ref", ProjectID: "proj", RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
	})
	if sessions.rollbackCanceled {
		t.Fatalf("rollback ran with a cancelled context; the spawned session would be orphaned")
	}
}


func TestEnsureConcurrentAssignmentsAlignToCaller(t *testing.T) {
	store, sessions := newFakeStore(), newFakeSessions()
	svc := New(store, sessions)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan string, 2)
	for _, assignment := range []string{"asg_1", "asg_2"} {
		wg.Add(1)
		go func(assignment string) {
			defer wg.Done()
			out, err := svc.Ensure(ctx, EnsureInput{
				ExternalRef: "ref", ProjectID: "proj", AssignmentRef: assignment,
				RequestFingerprint: "fp", Spawn: spawnConfig("proj"),
			})
			if err != nil {
				t.Errorf("ensure %s: %v", assignment, err)
				return
			}
			results <- string(out.External.AssignmentRef)
		}(assignment)
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	for assignment := range results {
		seen[assignment] = true
	}
	if !seen["asg_1"] || !seen["asg_2"] {
		t.Fatalf("returned assignments = %v, want each caller's own generation", seen)
	}
	if len(store.mappings) != 1 || len(sessions.spawned) != 1 {
		t.Fatalf("mappings=%d spawned=%d, want one session", len(store.mappings), len(sessions.spawned))
	}
}
