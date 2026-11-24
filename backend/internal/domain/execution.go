package domain

import "time"

// ExternalSession is the durable link between an external orchestrator's work
// item and the AO session that executes it. The orchestrator owns the work
// item's lifecycle; AO owns the session, worktree, and PR/CI facts. ExternalRef
// is the orchestrator's stable, globally unique id (namespaced by the
// orchestrator, for example "superagent:tk_123"); AssignmentRef is the current
// execution generation within that work item. RequestFingerprint covers the
// immutable launch parameters, so a retry with a changed launch request is a
// conflict rather than a silent reuse.
type ExternalSession struct {
	ExternalRef        string    `json:"externalRef"`
	ProjectID          ProjectID `json:"projectId"`
	SessionID          SessionID `json:"sessionId"`
	AssignmentRef      string    `json:"assignmentRef"`
	RequestFingerprint string    `json:"-"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
