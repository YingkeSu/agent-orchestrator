package controllers

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	execsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/execution"
)

// maxExternalEnsureBodyBytes bounds the ensure body. It carries a prompt but no
// attachments, so the spawn route's multi-megabyte budget would be wasteful.
const maxExternalEnsureBodyBytes = 64 << 10

// ExecutionService is the bridge's command/read surface. The production
// implementation is *execsvc.Service; tests use a fake.
type ExecutionService interface {
	Ensure(ctx context.Context, in execsvc.EnsureInput) (execsvc.Outcome, error)
	Get(ctx context.Context, externalRef string) (execsvc.View, error)
	List(ctx context.Context, project domain.ProjectID) ([]execsvc.View, error)
	Stop(ctx context.Context, externalRef, reason string) (execsvc.View, bool, error)
}

// ExecutionController exposes the bridge an external orchestrator uses to run a
// work item inside one AO project. It is a projection and idempotency layer over
// the session service, not a second session API.
type ExecutionController struct {
	Svc ExecutionService
}

// Register mounts the execution routes under /api/v1.
func (c *ExecutionController) Register(r chi.Router) {
	r.Put("/execution/sessions/{externalRef}", c.ensure)
	r.Get("/execution/sessions/{externalRef}", c.get)
	r.Post("/execution/sessions/{externalRef}/stop", c.stop)
	r.Get("/execution/sessions", c.list)
}

func (c *ExecutionController) ensure(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/execution/sessions/{externalRef}")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxExternalEnsureBodyBytes)
	var in EnsureExternalSessionRequest
	if err := decodeJSONStrict(r, &in); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	mode, err := domain.ParseSessionMode(string(in.Mode))
	if err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "SESSION_MODE_INVALID", err.Error(), nil)
		return
	}
	if len(in.Prompt) > maxPromptLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROMPT_TOO_LONG", "Prompt must be 16 KiB or fewer", nil)
		return
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if utf8.RuneCountInString(displayName) > maxDisplayNameLen {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "DISPLAY_NAME_TOO_LONG", "displayName must be 20 characters or fewer", nil)
		return
	}
	out, err := c.Svc.Ensure(r.Context(), execsvc.EnsureInput{
		ExternalRef:        chi.URLParam(r, "externalRef"),
		ProjectID:          in.ProjectID,
		AssignmentRef:      in.AssignmentRef,
		RequestFingerprint: in.RequestFingerprint,
		Spawn: ports.SpawnConfig{
			ProjectID:     in.ProjectID,
			IssueID:       in.IssueID,
			Kind:          domain.KindWorker,
			Harness:       in.Harness,
			Branch:        in.Branch,
			Prompt:        in.Prompt,
			RequestedMode: mode,
			DisplayName:   displayName,
			AgentConfig:   ports.AgentConfig{Model: in.Model},
		},
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	status := http.StatusOK
	if out.Created {
		status = http.StatusCreated
	}
	envelope.WriteJSON(w, status, EnsureExternalSessionResponse{
		Created: out.Created,
		Session: externalSessionView(execsvc.View{External: out.External, Session: out.Session}),
	})
}

func (c *ExecutionController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/execution/sessions/{externalRef}")
		return
	}
	view, err := c.Svc.Get(r.Context(), chi.URLParam(r, "externalRef"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, GetExternalSessionResponse{Session: externalSessionView(view)})
}

func (c *ExecutionController) stop(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/execution/sessions/{externalRef}/stop")
		return
	}
	var in StopExternalSessionRequest
	if err := decodeJSONStrict(r, &in); err != nil && !errors.Is(err, io.EOF) {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	view, stopped, err := c.Svc.Stop(r.Context(), chi.URLParam(r, "externalRef"), in.Reason)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, StopExternalSessionResponse{Stopped: stopped, Session: externalSessionView(view)})
}

func (c *ExecutionController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/execution/sessions")
		return
	}
	project := domain.ProjectID(strings.TrimSpace(r.URL.Query().Get("projectId")))
	if project == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "PROJECT_ID_REQUIRED", "projectId is required", nil)
		return
	}
	views, err := c.Svc.List(r.Context(), project)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	out := make([]ExternalSessionView, 0, len(views))
	for _, view := range views {
		out = append(out, externalSessionView(view))
	}
	envelope.WriteJSON(w, http.StatusOK, ListExternalSessionsResponse{Sessions: out})
}

func externalSessionView(view execsvc.View) ExternalSessionView {
	needsPerson, waitingReason := externalNeedsPerson(view.Session)
	prs := make([]ExternalSessionPRView, 0, len(view.Session.PRs))
	for _, pr := range view.Session.PRs {
		prs = append(prs, ExternalSessionPRView{
			URL:          pr.URL,
			Number:       pr.Number,
			State:        externalPRState(pr),
			CI:           pr.CI,
			Review:       pr.Review,
			Mergeability: pr.Mergeability,
		})
	}
	return ExternalSessionView{
		ExternalRef:   view.External.ExternalRef,
		AssignmentRef: view.External.AssignmentRef,
		ProjectID:     view.External.ProjectID,
		SessionID:     view.External.SessionID,
		CreatedAt:     view.External.CreatedAt,
		UpdatedAt:     view.External.UpdatedAt,
		Execution: ExternalSessionExecutionView{
			Status:                view.Session.Status,
			DisplayStatus:         view.Session.DisplayStatus,
			KanbanColumn:          view.Session.KanbanColumn,
			Activity:              view.Session.Activity.State,
			ActivityAt:            view.Session.Activity.LastActivityAt,
			IsTerminated:          view.Session.IsTerminated,
			NeedsPerson:           needsPerson,
			WaitingReason:         waitingReason,
			LatestAssistantUpdate: view.Session.Metadata.LatestAssistantUpdate,
			LatestUserPrompt:      view.Session.Metadata.LatestUserPrompt,
			Branch:                view.Session.Metadata.Branch,
			PRs:                   prs,
			UpdatedAt:             view.Session.UpdatedAt,
			Revision:              view.Session.UpdatedAt.UnixNano(),
		},
	}
}

// externalNeedsPerson maps AO's derived session state to the one thing the
// orchestrator must route to a person. CI failures and review changes are not
// human waits: AO's worker handles them, so they stay out of this signal.
func externalNeedsPerson(sess domain.Session) (bool, string) {
	if sess.Status == domain.StatusNeedsInput {
		return true, "question"
	}
	switch sess.Activity.State {
	case domain.ActivityBlocked:
		return true, "approval"
	case domain.ActivityWaitingInput:
		return true, "input"
	}
	return false, ""
}

func externalPRState(pr domain.PRFacts) string {
	switch {
	case pr.Draft:
		return "draft"
	case pr.Merged:
		return "merged"
	case pr.Closed:
		return "closed"
	default:
		return "open"
	}
}
