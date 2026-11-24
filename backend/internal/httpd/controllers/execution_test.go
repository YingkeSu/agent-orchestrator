package controllers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/config"
	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	execsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/execution"
	"github.com/aoagents/agent-orchestrator/backend/pkg/contract"
)

type fakeExecution struct {
	ensureOut  execsvc.Outcome
	ensureErr  error
	getView    execsvc.View
	getErr     error
	stopView   execsvc.View
	stopped    bool
	stopErr    error
	listViews  []execsvc.View
	listErr    error
	lastEnsure execsvc.EnsureInput
	lastReason string
}

func (f *fakeExecution) Ensure(_ context.Context, in execsvc.EnsureInput) (execsvc.Outcome, error) {
	f.lastEnsure = in
	return f.ensureOut, f.ensureErr
}

func (f *fakeExecution) Get(context.Context, string) (execsvc.View, error) {
	return f.getView, f.getErr
}

func (f *fakeExecution) List(context.Context, domain.ProjectID) ([]execsvc.View, error) {
	return f.listViews, f.listErr
}

func (f *fakeExecution) Stop(_ context.Context, _ string, reason string) (execsvc.View, bool, error) {
	f.lastReason = reason
	return f.stopView, f.stopped, f.stopErr
}

func newExecutionServer(t *testing.T, svc controllers.ExecutionService) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(httpd.NewRouterWithControl(config.Config{}, log, nil, httpd.APIDeps{
		Execution: svc,
	}, httpd.ControlDeps{}))
	t.Cleanup(srv.Close)
	return srv
}

func executionView() execsvc.View {
	now := time.Date(2026, 11, 25, 2, 0, 0, 0, time.UTC)
	return execsvc.View{
		External: domain.ExternalSession{
			ExternalRef: "superagent:tk_1", ProjectID: "proj", SessionID: "proj-1",
			AssignmentRef: "asg_1", CreatedAt: now, UpdatedAt: now,
		},
		Session: domain.Session{
			SessionRecord: domain.SessionRecord{
				ID: "proj-1", ProjectID: "proj", UpdatedAt: now,
				Activity: domain.Activity{State: domain.ActivityBlocked, LastActivityAt: now},
				Metadata: domain.SessionMetadata{LatestAssistantUpdate: "need approval"},
			},
			Status:        domain.StatusNeedsInput,
			DisplayStatus: contract.DisplayBlocked,
			KanbanColumn:  contract.KanbanBuilding,
			PRs: []domain.PRFacts{{
				URL: "https://github.com/acme/repo/pull/7", Number: 7, CI: contract.CIFailing,
				Review: contract.ReviewChangesRequest, Mergeability: contract.MergeConflicting,
			}},
		},
	}
}

func TestExecutionEnsureReturnsCreatedSession(t *testing.T) {
	svc := &fakeExecution{ensureOut: execsvc.Outcome{
		External: executionView().External, Session: executionView().Session, Created: true,
	}}
	srv := newExecutionServer(t, svc)
	body, status, headers := doRequest(t, srv, "PUT", "/api/v1/execution/sessions/superagent:tk_1", `{"projectId":"proj","assignmentRef":"asg_1","requestFingerprint":"fp","prompt":"do it","displayName":"fix-login"}`)
	assertJSON(t, headers)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", status, body)
	}
	var out controllers.EnsureExternalSessionResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Created || out.Session.ExternalRef != "superagent:tk_1" || out.Session.SessionID != "proj-1" {
		t.Fatalf("response = %+v", out)
	}
	if !out.Session.Execution.NeedsPerson || out.Session.Execution.WaitingReason != "question" {
		t.Fatalf("needsPerson = %+v, want question", out.Session.Execution)
	}
	if len(out.Session.Execution.PRs) != 1 || out.Session.Execution.PRs[0].State != "open" {
		t.Fatalf("prs = %+v", out.Session.Execution.PRs)
	}
	if svc.lastEnsure.Spawn.ProjectID != "proj" || svc.lastEnsure.Spawn.DisplayName != "fix-login" {
		t.Fatalf("ensure input = %+v", svc.lastEnsure)
	}
}

func TestExecutionEnsureReusesExistingSession(t *testing.T) {
	svc := &fakeExecution{ensureOut: execsvc.Outcome{
		External: executionView().External, Session: executionView().Session, Created: false,
	}}
	srv := newExecutionServer(t, svc)
	_, status, _ := doRequest(t, srv, "PUT", "/api/v1/execution/sessions/superagent:tk_1", `{"projectId":"proj","requestFingerprint":"fp"}`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
}

func TestExecutionGetUnknownIs404(t *testing.T) {
	svc := &fakeExecution{getErr: apierr.NotFound("EXTERNAL_SESSION_NOT_FOUND", "Unknown external session")}
	srv := newExecutionServer(t, svc)
	body, status, headers := doRequest(t, srv, "GET", "/api/v1/execution/sessions/missing", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusNotFound, "EXTERNAL_SESSION_NOT_FOUND")
}

func TestExecutionStopIsIdempotentAndCarriesReason(t *testing.T) {
	svc := &fakeExecution{stopView: executionView(), stopped: true}
	srv := newExecutionServer(t, svc)
	body, status, headers := doRequest(t, srv, "POST", "/api/v1/execution/sessions/superagent:tk_1/stop", `{"reason":"superseded"}`)
	assertJSON(t, headers)
	if status != http.StatusOK {
		t.Fatalf("status = %d: %s", status, body)
	}
	if svc.lastReason != "superseded" {
		t.Fatalf("reason = %q", svc.lastReason)
	}
	var out controllers.StopExternalSessionResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Stopped {
		t.Fatalf("stopped = false")
	}
}

func TestExecutionListRequiresProject(t *testing.T) {
	srv := newExecutionServer(t, &fakeExecution{})
	body, status, headers := doRequest(t, srv, "GET", "/api/v1/execution/sessions", "")
	assertJSON(t, headers)
	assertErrorCode(t, body, status, http.StatusBadRequest, "PROJECT_ID_REQUIRED")
}
