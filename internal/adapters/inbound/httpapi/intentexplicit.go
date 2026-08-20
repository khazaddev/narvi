package httpapi

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/app/intentclassifier"
	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
	"github.com/khazaddev/narvi/internal/platform"
)

// recordExplicitIntentDecision persists §8.3's own §18.4 "explicit"
// source decision record for a WEB session: a human's own explicit
// plan/build toggle (restdtos.CreateSessionRequest.PlanMode) is a real,
// architecturally-known signal the moment a session is created --
// CreateSession (create.go) never calls IntentClassifier.Classify at all
// for this surface (§18.3: "A decision record supplied by the calling
// surface itself is honored ONLY for spawn_source values architecturally
// capable of having classified it themselves"). intentSvc is nil-safe: a
// nil value (classifier not wired, e.g. in a test rig) simply skips this
// entirely.
//
// surface MUST be a value the CALLER itself knows to be true from its own
// server-side vantage point -- e.g. a literal like "web" hardcoded at a
// call site that is structurally only ever reachable as that surface --
// NEVER a client-supplied claim (a JSON request-body field, a header,
// etc.) forwarded as-is. §18.4: "this check is server-side and never
// trusts a client-supplied claim." Passing through an untrusted value
// here would let a caller manufacture an "explicit" decision record
// attributed to a surface it was never actually classified by.
//
// Target is deliberately left empty: nothing about the web UI's own
// PlanMode toggle carries a review-vs-request signal (there is currently
// no consumer of Target for ANY surface, per this Step's own scope note
// -- this just classifies/records faithfully what IS known: Mode only).
func recordExplicitIntentDecision(ctx context.Context, intentSvc *intentclassifier.Service, sessionID pgtype.UUID, surface string, planMode bool) {
	if intentSvc == nil {
		return
	}

	mode := intentdomain.ModeBuild
	if planMode {
		mode = intentdomain.ModePlan
	}

	if _, err := intentSvc.RecordDecision(ctx, sessionID, intentdomain.IntentDecisionRecord{
		Surface:        surface,
		Source:         intentdomain.RecordSourceExplicit,
		Mode:           mode,
		DecidedAt:      time.Now(),
		DecidedAtStage: intentdomain.DecidedAtStageCreate,
	}); err != nil {
		platform.Logger(ctx).Warn("httpapi: record explicit intent decision failed", "error", err, "session_id", sessionID)
	}
}
