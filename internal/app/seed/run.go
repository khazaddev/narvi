package seed

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/narvidev/narvi/internal/domain/seedmanifest"
	"github.com/narvidev/narvi/internal/platform"
)

// Run reconciles m against the database reachable through deps, section
// by section (participants, secrets, automations, repoSettings,
// rwxPreview), in that order -- deliberately participants FIRST: a
// secret/automation/repo_setting section never depends on which
// participants exist, but reading the report top-to-bottom in manifest
// order is what an operator scanning for "did my own admin grant land"
// expects.
//
// Every item is processed independently: one item's error never aborts
// the rest of the run (see doc.go's own "each write is its own
// transaction" design) -- Run's own returned error is reserved for a
// failure that makes the WHOLE run meaningless to continue (the
// automations existence-check query itself failing; see
// loadExistingAutomationsByName below). Per-item failures are reported
// as Item{Outcome: OutcomeError} inside the returned *Report; check
// Report.HasErrors() for the caller's own exit-code decision.
//
// A fresh correlation id is minted once per Run call and threaded onto
// ctx (platform.WithCorrelationID) before any item is processed, so
// every audit_log row one seed invocation produces shares one
// correlation id -- visible together in Settings -> Members -> Audit
// log (§13.3).
func Run(ctx context.Context, deps Deps, m *seedmanifest.Manifest, dryRun bool) (*Report, error) {
	ctx = platform.WithCorrelationID(ctx, uuid.NewString())

	report := &Report{DryRun: dryRun}

	for _, p := range m.Participants {
		report.Items = append(report.Items, seedParticipant(ctx, deps, p, dryRun))
	}

	for _, s := range m.Secrets {
		report.Items = append(report.Items, seedSecret(ctx, deps, s, dryRun))
	}

	if len(m.Automations) > 0 {
		existingByName, err := loadExistingAutomationsByName(ctx, deps)
		if err != nil {
			return report, fmt.Errorf("seed: list existing automations: %w", err)
		}
		for _, a := range m.Automations {
			report.Items = append(report.Items, seedAutomation(ctx, deps, a, existingByName, dryRun))
		}
	}

	for _, rs := range m.RepoSettings {
		report.Items = append(report.Items, seedRepoSetting(ctx, deps, rs, dryRun))
	}

	for _, rp := range m.RWXPreview {
		report.Items = append(report.Items, seedRWXPreview(ctx, deps, rp, dryRun))
	}

	return report, nil
}

// loadExistingAutomationsByName lists every automation ONCE (createdBy/
// status both unfiltered -- pgtype.UUID{} carries Valid: false, matching
// ListAutomations' own sqlc.narg "IS NULL OR ..." idiom for "no filter")
// and indexes it by Name, so seedAutomation's own create-if-absent check
// is a single map lookup per manifest entry rather than one query per
// entry. automations.name has no UNIQUE constraint at the database layer
// (see internal/domain/seedmanifest's own Automation doc comment and
// doc.go's own idempotency writeup) -- two concurrent seed runs
// targeting the same manifest could each decide "absent" and each
// create a duplicate. This tool is meant to be run by one operator at a
// time, the same expectation a migration run carries; it does not add
// its own distributed lock on top of that expectation.
func loadExistingAutomationsByName(ctx context.Context, deps Deps) (map[string]sqlcgen.Automation, error) {
	rows, err := deps.Automations.List(ctx, pgtype.UUID{}, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]sqlcgen.Automation, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out, nil
}
