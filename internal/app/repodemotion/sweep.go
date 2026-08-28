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
// skipped for THAT session only, never treated as a match: the same
// "a thing we cannot evaluate must not be acted on" posture postgres.
// OutboxStore.ResolveEffectiveMode already applies to its own identical
// per-session repo-parsing step, applied here to a decision that TERMINATES
// a sandbox rather than merely suppressing a notification -- an unreadable
// repo list is exactly the case demanding the MOST caution, not the
// least, so it is logged by the caller (via the returned error, if
// parsing fails at the batch level) rather than silently matched.
func Sweep(ctx context.Context, sandboxes *postgres.SandboxStore, repoFullName string) (int, error) {
	rows, err := sandboxes.ListLiveWithSessionRepos(ctx)
	if err != nil {
		return 0, fmt.Errorf("repodemotion: list live sandboxes: %w", err)
	}

	marked := 0
	for _, row := range rows {
		names, ok := sessionRepoFullNames(row.Repos)
		if !ok {
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
