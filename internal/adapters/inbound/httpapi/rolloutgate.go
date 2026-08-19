// This file (rolloutgate.go) implements Step 76's own ("feature-flagged
// cohort rollout of sessions, with documented rollback", §10 Phase 6,
// §32) primary, session-creation-time gate: checkRolloutGate, called from
// CreateSessionOnTx (create.go) after validateCreateSessionRequest and
// before the environment/session inserts, on the SAME transaction that is
// about to insert the session.
//
// This is HALF of §32's "fail-closed, twice" pair -- the dispatch-time
// re-check (internal/app/sessionactor's own tryPlanSpawn, beside
// refuseIfSubstrateUnsupported) is the other half, and is what makes
// rollback real: without it, de-enrolling a repo would leave an existing
// PR review session respawning sandboxes forever on re-review turns,
// since those ride the REUSE branch and the actor's own dispatch loop,
// never this creation funnel. Both sites share internal/domain/rollout's
// ONE pure decision (Decide) -- this file's only job is the I/O half:
// resolving each named repo to a trusted, host-verified identity, reading
// its repo_settings.sessions_enabled row (fail-closed), and turning the
// result into a *CreateSessionError every caller of CreateSessionOnTx
// already knows how to propagate.

package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/rollout"
	"github.com/khazaddev/narvi/internal/platform"
)

// rolloutGateMeterName is this package's own OTel meter name for the
// rollout-refusal counter, mirroring cloudIdentityMeterName's own
// "narvi/httpapi-<concern>" precedent exactly (cloudidentitymetrics.go).
const rolloutGateMeterName = "narvi/httpapi-rolloutgate"

// sessionRolloutRefusedTotalCounter is resolved LAZILY, on first use
// (sync.OnceValue) -- mirrors cloudIdentityMintTotalCounter's own doc
// comment exactly: CreateSessionOnTx is a free function with no
// per-process constructor object to anchor eager construction to, and
// resolving otel.Meter at package-init time would permanently bind this
// instrument to whatever MeterProvider happens to be globally registered
// at that moment (which main.go's own real OTel SDK setup may not have
// installed yet).
var sessionRolloutRefusedTotalCounter = sync.OnceValue(newSessionRolloutRefusedTotalCounter)

func newSessionRolloutRefusedTotalCounter() metric.Int64Counter {
	c, err := otel.Meter(rolloutGateMeterName).Int64Counter(
		"session_rollout_refused_total",
		metric.WithDescription("Count of every session-creation attempt refused by Step 76's cohort-rollout gate (§32) because a named repo was not enrolled (repo_settings.sessions_enabled). Tagged by the \"spawn_source\" attribute -- see checkRolloutGate's own doc comment."),
		metric.WithUnit("{refusal}"),
	)
	if err != nil {
		// Structurally cannot fail for a fixed, well-formed instrument
		// name -- logged defensively anyway, mirroring
		// newCloudIdentityMintTotalCounter's own identical precedent.
		platform.Logger(context.Background()).Error("httpapi: construct session_rollout_refused_total counter failed", "error", err)
	}
	return c
}

// recordRolloutRefusal increments the refusal counter by one, tagged by
// spawnSource (session_spawn_source's own wire value -- web/slack/linear/
// github), mirroring recordCloudIdentityMint's own "kind" attribute
// precedent.
func recordRolloutRefusal(ctx context.Context, spawnSource string) {
	sessionRolloutRefusedTotalCounter().Add(ctx, 1, metric.WithAttributes(attribute.String("spawn_source", spawnSource)))
}

// resolveRolloutRepoFullName resolves rawURL to a trusted "owner/repo"
// identity for the rollout gate specifically -- reposource.CheckRepoHost
// FIRST, then ParseOwnerRepo, exactly the pairing every other
// ParseOwnerRepo call site in this codebase already uses (app/
// sessionactor's pushpr.go/contractdrift.go/imageresolve.go, app/
// outboxworker's sentinelautofix.go, app/imagebuild's builder.go). This
// pairing is load-bearing, not defensive decoration: ParseOwnerRepo is
// deliberately host-agnostic (its own doc comment: "it never inspects
// rawURL's host at all"), so https://evil.example/acme/widgets.git
// derives the SAME "acme/widgets" a genuine https://github.com/acme/
// widgets.git would -- without the host check running FIRST, a repo
// enrolled under github.com could be spoofed by ANY host that happens to
// reuse its owner/repo path. ok is false for either a rejected host or an
// unparseable path -- the caller folds either into RepoAdmission.
// Enrolled == false (fail-closed), never distinguishing the two beyond
// its own log line.
func resolveRolloutRepoFullName(rawURL string) (fullName string, ok bool) {
	if err := reposource.CheckRepoHost(rawURL, ports.SupportedSourceControlHosts()...); err != nil {
		return "", false
	}
	owner, repo, err := reposource.ParseOwnerRepo(rawURL)
	if err != nil {
		return "", false
	}
	return owner + "/" + repo, true
}

// checkRolloutGate is §32's own primary gate. mode is platform.Config.
// RolloutMode, verbatim -- when it is anything other than rollout.
// ModeCohort (including the unset/default rollout.ModeOpen), this
// function returns nil WITHOUT iterating repos, resolving any URL, or
// touching repoSettings at all: §32's own "byte-for-byte no-op" property
// for every existing deployment and CI depends on this short-circuit
// running before any I/O, not just on Decide's own pure short-circuit
// (internal/domain/rollout.Decide has the identical guard, but this
// function must not even READ repo_settings to get there).
//
// In rollout.ModeCohort, every repo in repos is resolved and looked up,
// on tx (repoSettings.WithTx(tx).Get) -- fail-closed per §32: an absent
// row (pgx.ErrNoRows), any OTHER read error, or an unresolvable/
// unsupported-host URL (resolveRolloutRepoFullName's own ok=false) all
// fold into RepoAdmission.Enrolled == false identically. §32's own
// reasoning for why this is nearly free: this read runs inside the SAME
// transaction that was about to insert the session two statements later,
// on the same Postgres -- there is no real state where repo_settings is
// unreadable but that insert would have succeeded.
//
// On refusal, this logs a structured Warn (repo, spawn source, mode --
// point 8) and increments session_rollout_refused_total, but writes NO
// audit_log row: this codebase's own audit_log table records completed
// STATE CHANGES only, never a refusal of any kind (reposettings.go's own
// logUnknownRepoRefusal doc comment states this convention explicitly;
// mirrored here, not reinvented). The returned *CreateSessionError
// carries RolloutRefusal: true (CreateSessionError's own doc comment) so
// every caller of CreateSessionOnTx can distinguish this permanent policy
// refusal from a transient failure structurally.
func checkRolloutGate(ctx context.Context, tx pgx.Tx, repoSettings *postgres.RepoSettingsStore, mode platform.RolloutMode, req restdtos.CreateSessionRequest) *CreateSessionError {
	if mode != rollout.ModeCohort {
		return nil
	}

	logger := platform.Logger(ctx)

	admissions := make([]rollout.RepoAdmission, 0, len(req.Repos))
	for _, repo := range req.Repos {
		fullName, resolved := resolveRolloutRepoFullName(repo.Url)
		if !resolved {
			logger.Warn("httpapi: rollout gate: repo url could not be resolved to a trusted, host-verified owner/repo identity; treating as not enrolled",
				"url", repo.Url, "spawn_source", string(req.SpawnSource), "rollout_mode", string(mode))
			admissions = append(admissions, rollout.RepoAdmission{FullName: repo.Url, Enrolled: false})
			continue
		}

		row, err := repoSettings.WithTx(tx).Get(ctx, fullName)
		switch {
		case err == nil:
			admissions = append(admissions, rollout.RepoAdmission{FullName: fullName, Enrolled: row.SessionsEnabled})
		case errors.Is(err, pgx.ErrNoRows):
			admissions = append(admissions, rollout.RepoAdmission{FullName: fullName, Enrolled: false})
		default:
			// A genuine read error, on the transaction about to insert
			// this very session -- fail-closed (§32, §62 finding C3's own
			// precedent: widening policy on a degraded read is
			// backwards), never treated as "no row, so unenrolled" without
			// comment: logged distinctly so an operator can tell a real
			// Postgres problem apart from an ordinary not-yet-enrolled
			// repo.
			logger.Warn("httpapi: rollout gate: read repo_settings failed; failing closed (treating as not enrolled)",
				"repo", fullName, "error", err, "spawn_source", string(req.SpawnSource), "rollout_mode", string(mode))
			admissions = append(admissions, rollout.RepoAdmission{FullName: fullName, Enrolled: false})
		}
	}

	decision := rollout.Decide(mode, admissions)
	if decision.Admitted {
		return nil
	}

	logger.Warn("httpapi: rollout gate: session creation refused, repo not enrolled",
		"repo", decision.RepoFullName, "spawn_source", string(req.SpawnSource), "rollout_mode", string(mode))
	recordRolloutRefusal(ctx, string(req.SpawnSource))

	return &CreateSessionError{
		Status:         http.StatusForbidden,
		Message:        "repository not enrolled: " + decision.RepoFullName,
		RolloutRefusal: true,
	}
}
