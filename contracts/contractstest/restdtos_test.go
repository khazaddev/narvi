package contractstest

import (
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
)

// rest/v1/dtos.schema.json deliberately has no top-level oneOf (§6.3:
// "independent named payloads, not a discriminated union"), so each DTO is
// validated against its own $defs entry.
const restDTOsSchemaPath = "rest/v1/dtos.schema.json"

func TestSessionRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/Session")

	createdAt := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 16, 10, 30, 0, 0, time.UTC)

	t.Run("ActiveSession", func(t *testing.T) {
		title := "Fix the failing test"
		createdBy := testSessionID
		roundTrip(t, sch, restdtos.Session{
			Id:            testSessionID,
			Title:         &title,
			Status:        restdtos.SessionStatusActive,
			FailureReason: nil,
			Archived:      false,
			SpawnSource:   restdtos.SessionSpawnSourceWeb,
			CreatedBy:     &createdBy,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
	})

	t.Run("FailedSessionWithReason", func(t *testing.T) {
		// status/failureReason/spawnSource enums MUST match
		// migrations/000004_sessions.up.sql exactly (session_status,
		// session_failure_reason, session_spawn_source).
		roundTrip(t, sch, restdtos.Session{
			Id:            testSessionID,
			Title:         nil,
			Status:        restdtos.SessionStatusFailed,
			FailureReason: &restdtos.SessionFailureReason{Value: "timeout"},
			Archived:      true,
			SpawnSource:   restdtos.SessionSpawnSourceGithub,
			CreatedBy:     nil,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
	})
}

func TestCreateSessionRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateSessionRequest")

	title := "New session"
	prompt := "implement the feature"
	modelID := "claude-sonnet-5"
	branch := "main"

	roundTrip(t, sch, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceSlack,
		Title:       &title,
		Prompt:      &prompt,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: &branch},
		},
		ModelId:  &modelID,
		PlanMode: true,
	})
}

func TestCreateSessionRequestRoundTrip_NullOptionals(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateSessionRequest")

	// title/prompt/modelId null, and no first turn dispatched (§5.1 "single
	// CreateSessionRequest" shape used by every ingress surface).
	roundTrip(t, sch, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceLinear,
		Title:       nil,
		Prompt:      nil,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: nil},
		},
		ModelId:  nil,
		PlanMode: false,
	})
}

func TestWSTokenResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/WSTokenResponse")

	roundTrip(t, sch, restdtos.WSTokenResponse{
		Token:     "ws-token-abc",
		ExpiresAt: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC),
	})
}
