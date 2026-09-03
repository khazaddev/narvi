package reviewtriage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/narvidev/narvi/internal/domain/reviewtriage"
	"github.com/narvidev/narvi/internal/platform"
)

// ResolveProvenance is §26.3's own "provenance: Narvi-authored vs human,
// and the authoring model" signal -- best-effort, NEVER errors (mirrors
// ComputeDecision's own never-throw contract, compute.go): the review
// depth decision this signal rides alongside must never be delayed or
// blocked by a degraded authorship lookup.
//
// Reuses the SAME "an artifacts row of type 'pr' exists for this exact
// html_url" fact internal/app/outboxworker's own isPlatformAuthored (and
// internal/app/decisioninbox's own identical twin) already establish --
// see that function's own doc comment, artifacts.GetPRArtifactByURL's
// generated doc comment, and internal/app/sessionactor/pushpr.go's own
// recordPRArtifact (the ONE place such a row is ever written) for the
// full "why" this is the authoritative signal. AuthoringModel is that
// SAME session's own sessions.build_model_id -- empty when the session
// never had an explicit build model set, which is a legitimate, common
// value, never an error.
func ResolveProvenance(ctx context.Context, deps Deps, repoFullName string, prNumber int32) reviewtriage.Provenance {
	if deps.Artifacts == nil || deps.Sessions == nil {
		return reviewtriage.Provenance{}
	}
	logger := platform.Logger(ctx)

	artifact, err := deps.Artifacts.GetPRArtifactByURL(ctx, pullRequestHTMLURL(repoFullName, prNumber))
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn("reviewtriage: resolve provenance: read pr artifact failed, treating as human-authored", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
		}
		// pgx.ErrNoRows: no Narvi session ever pushed/opened this PR -- a
		// confirmed, common negative, never an error.
		return reviewtriage.Provenance{}
	}

	session, err := deps.Sessions.Get(ctx, artifact.SessionID)
	if err != nil {
		logger.Warn("reviewtriage: resolve provenance: read authoring session failed, authoring model left blank", "error", err, "repo_full_name", repoFullName, "pr_number", prNumber)
		return reviewtriage.Provenance{NarviAuthored: true}
	}

	authoringModel := ""
	if session.BuildModelID != nil {
		authoringModel = *session.BuildModelID
	}
	return reviewtriage.Provenance{NarviAuthored: true, AuthoringModel: authoringModel}
}
