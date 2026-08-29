package repodemotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/platform"
)

// Sweep implements this package's own primary job (see doc.go): given
// repoFullName ("owner/repo") that has JUST been demoted (a genuine
// live_egress_enabled true->false transition, the caller's own job to
// detect), find every currently-live sandbox whose owning session names
// that repository and (1) flag it for real termination
// (SandboxStore.MarkDemotionTerminationRequested -- acted on by
// internal/app/reconciler.Reconciler's own new demotion-sweep tick, never
// by this function, which never touches a real ports.SandboxProvider) and
// (2) cancel any push/PR decision currently outstanding on it
// (SandboxStore.CancelPendingPush).
//
// Returns the number of sandboxes flagged, for the caller's own audit-log
// detail -- zero (with a nil error) is the ordinary, expected outcome for
// a repository with no currently-live sandbox at all.
//
// A sandbox whose own session names repositories this function cannot
// read (malformed JSON, an unsupported host, an unparseable clone URL) is
// skipped for THAT session and LOUDLY LOGGED. That is a trade-off, not a
// maximally safe choice, and it is worth stating in that direction:
//
// Skipping is the less safe option for §30's own guarantee. The sandbox
// might well be one of this repo's, and if it is, it keeps whatever
// credential it holds for the ScmCredentialTTL window -- which is the
// exact leak this sweep exists to close. The alternative, flagging every
// unreadable-repo sandbox on any demotion, terminates running sessions
// that have nothing to do with this repository, and does so on no
// evidence at all.
//
// Skipping wins only because this sweep is defense-in-depth: §30.4 is
// explicit that the structural control is the read-only credential, and
// the sweep shortens an exposure window rather than creating the
// guarantee. A layer that is not load-bearing may take the
// non-destructive branch. It may not do so silently, which is why each
// skip is a warning naming the session -- an operator can act on what
// this function deliberately would not.
func Sweep(ctx context.Context, sandboxes *postgres.SandboxStore, repoFullName string) (int, error) {
	rows, err := sandboxes.ListLiveWithSessionRepos(ctx)
	if err != nil {
		return 0, fmt.Errorf("repodemotion: list live sandboxes: %w", err)
	}

	marked := 0
	for _, row := range rows {
		names, ok := sessionRepoFullNames(row.Repos)
		if !ok {
			platform.Logger(ctx).Warn("repodemotion: a live sandbox's session repos could not be read; NOT flagging it for termination on this demotion (see Sweep's own doc comment for why this direction, and what it costs)",
				"session_id", row.SessionID.String(), "demoted_repo", repoFullName)
			continue
		}

		matched := false
		for _, name := range names {
			if name == repoFullName {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		if _, err := sandboxes.MarkDemotionTerminationRequested(ctx, row.SessionID); err != nil {
			return marked, fmt.Errorf("repodemotion: mark demotion termination requested for session %s: %w", row.SessionID.String(), err)
		}
		if _, err := sandboxes.CancelPendingPush(ctx, row.SessionID); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			// pgx.ErrNoRows (unwrapped) means this sandbox simply has no
			// push currently outstanding -- CancelSandboxPendingPush's own
			// generated doc comment, "nothing to cancel", never an error.
			return marked, fmt.Errorf("repodemotion: cancel pending push for session %s: %w", row.SessionID.String(), err)
		}
		marked++
	}
	return marked, nil
}

// sessionRepoFullNames derives every "owner/repo" full name from a
// session's own raw sessions.repos column (JSONB) -- pure, no I/O.
// Deliberately duplicated rather than imported, matching this codebase's
// own established convention of a small, private per-package repo-JSON
// helper (postgres.outboxShadow's own sessionRepoFullNames; app/
// sessionactor's own reposFromJSON/rolloutDecisionForSession) rather than
// one shared repo-parsing package no Step has ever introduced.
//
// The second return value distinguishes "this session names no
// repositories" (an empty slice, parsed fine -- never matches anything)
// from "these repositories could not be read" (false, skipped by this
// package's one caller, Sweep) -- a single empty result would collapse
// those two, different facts.
//
// reposource.CheckRepoHost is checked BEFORE ParseOwnerRepo, mirroring
// the audit-hardening precedent app/sessionactor's own
// rolloutDecisionForSession, imageresolve.go, pushpr.go, and
// contractdrift.go all already established: ParseOwnerRepo is
// deliberately host-agnostic, so without this pairing a spoofed
// cross-host URL sharing an enrolled repo's own owner/repo path would
// silently match a demotion this function was never asked to sweep for.
func sessionRepoFullNames(rawRepos []byte) ([]string, bool) {
	if len(rawRepos) == 0 {
		return nil, true
	}
	var repos []restdtos.CreateSessionRequestReposElem
	if err := json.Unmarshal(rawRepos, &repos); err != nil {
		return nil, false
	}
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		if err := reposource.CheckRepoHost(r.Url, ports.SupportedSourceControlHosts()...); err != nil {
			return nil, false
		}
		owner, repo, err := reposource.ParseOwnerRepo(r.Url)
		if err != nil {
			return nil, false
		}
		names = append(names, owner+"/"+repo)
	}
	return names, true
}
