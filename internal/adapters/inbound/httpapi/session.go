package httpapi

import (
	"encoding/json"
	"log/slog"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// sessionToDTO converts a stored sqlcgen.Session row into its REST wire
// shape (restdtos.Session, contracts/rest/v1/dtos.schema.json). Explicit
// conversions are needed for the enum fields -- sqlcgen.SessionStatus/
// SessionSpawnSource and their restdtos counterparts are two DISTINCT
// named string types generated from two independent schemas (a Postgres
// enum vs a JSON Schema enum) that happen to share the same underlying
// type and allowed values, per this schema's own "Enums here MUST match
// the Postgres enums ... exactly" requirement -- and for failureReason/
// createdBy's own nullability representations (pgtype's own
// nullable-value convention vs restdtos' plain-pointer/wrapper-struct
// convention).
//
// sandboxStatus is always nil here -- see Session.sandboxStatus's own
// schema doc comment: GetSession (single-session view) deliberately never
// populates it, only ListSessions (sessionsToListDTO below) does. Every
// call site of THIS function is GetSession/CreateSession, never the list
// endpoint, so that omission is enforced by construction, not by a caller
// remembering to leave a field unset.
func sessionToDTO(s sqlcgen.Session) restdtos.Session {
	return sessionRowToDTO(s, nil)
}

// sessionRowToDTO is sessionToDTO's own shared core, parameterized on the
// sandbox status a caller already has in hand (nil when none, e.g. every
// sessionToDTO call site above) -- sessionsToListDTO (list.go) is the
// other, sandbox-status-bearing caller.
func sessionRowToDTO(s sqlcgen.Session, sandboxStatus *sqlcgen.SandboxStatus) restdtos.Session {
	var createdBy *string
	if s.CreatedBy.Valid {
		str := s.CreatedBy.String()
		createdBy = &str
	}

	var failureReason *restdtos.SessionFailureReason
	if s.FailureReason != nil {
		failureReason = &restdtos.SessionFailureReason{Value: string(*s.FailureReason)}
	}

	var sandboxStatusDTO *restdtos.SessionSandboxStatus
	if sandboxStatus != nil {
		sandboxStatusDTO = &restdtos.SessionSandboxStatus{Value: string(*sandboxStatus)}
	}

	return restdtos.Session{
		Id:            s.ID.String(),
		Title:         s.Title,
		Status:        restdtos.SessionStatus(s.Status),
		FailureReason: failureReason,
		Archived:      s.Archived,
		SpawnSource:   restdtos.SessionSpawnSource(s.SpawnSource),
		CreatedBy:     createdBy,
		CreatedAt:     s.CreatedAt.Time,
		UpdatedAt:     s.UpdatedAt.Time,
		Repos:         decodeSessionRepos(s.Repos),
		SandboxStatus: sandboxStatusDTO,
	}
}

// decodeSessionRepos unmarshals sessions.repos' own raw jsonb bytes
// (sqlcgen.Session.Repos, migrations/000018_session_repos.up.sql --
// "position 0 = primary; repos are always a list") into the wire shape.
// Every session row this codebase can produce today has a well-formed,
// non-null repos value (CreateSession's own COALESCE default is
// '[]'::jsonb, never SQL NULL, and CreateSession's own httpapi validation
// requires a non-empty repos on every NEW session) -- a decode failure
// here is therefore a genuine "this codebase wrote invalid jsonb into its
// own column" defect, not an expected input-validation case, so it
// degrades to an empty list (never a 500) with a logged warning rather
// than either panicking or silently pretending the failure never
// happened.
func decodeSessionRepos(raw []byte) []restdtos.AutomationReposElem {
	repos := []restdtos.AutomationReposElem{}
	if len(raw) == 0 {
		return repos
	}
	if err := json.Unmarshal(raw, &repos); err != nil {
		// No request-scoped ctx is threaded this deep (sessionToDTO/
		// sessionRowToDTO take none either) -- this is a defensive
		// fallback for a row this codebase should never actually write
		// (see this function's own top comment), not a request-scoped
		// error worth platform.Logger's correlation-id enrichment.
		slog.Default().Error("httpapi: session.repos is not valid JSON (corrupt row?)", "error", err)
		return []restdtos.AutomationReposElem{}
	}
	return repos
}
