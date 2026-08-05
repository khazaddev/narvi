//go:build integration

// This file covers Step 52's own ("automations: triggers & extras", §8.4)
// additions to the engine Step 51 already built: the cron trigger pump
// (triggerpump.go), sandboxSettings honored on automation sessions
// (fanout.go's own applySandboxSettings), per-automation env vars threaded
// into the dispatched prompt (fanout.go's own buildRunPrompt), and
// last_run/artifact_summary populated at close-out (closeout.go's own
// recordLastRun) -- mirrors automation_integration_test.go's own black-box,
// real-Engine-against-a-real-Postgres shape exactly, reusing its shared
// testFixture/newFixture.
package automation_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/automation"
	domainautomation "github.com/khazaddev/narvi/internal/domain/automation"
)

// createCronAutomation inserts an automation with TriggerTypeCron and the
// given schedule, one target, and no prompt -- mirrors f.createAutomation's
// own shape but with an explicit trigger_type/trigger_config rather than
// 'manual'/'{}'.
func (f *testFixture) createCronAutomation(t *testing.T, name, schedule string, status sqlcgen.AutomationStatus) sqlcgen.Automation {
	t.Helper()
	ctx := context.Background()

	targets := []domainautomation.Target{{Name: "repo", URL: "https://github.com/acme/repo"}}
	reposJSON, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	triggerConfigJSON, err := json.Marshal(map[string]string{"schedule": schedule})
	if err != nil {
		t.Fatalf("marshal trigger config: %v", err)
	}

	row, err := f.automations.Create(ctx, sqlcgen.CreateAutomationParams{
		Name: name, Repos: reposJSON, CreatedBy: pgtype.UUID{},
		TriggerType: sqlcgen.AutomationTriggerTypeCron, TriggerConfig: triggerConfigJSON, EnvVars: []byte("[]"),
	})
	if err != nil {
		t.Fatalf("create cron automation: %v", err)
	}

	if status == sqlcgen.AutomationStatusPaused {
		if _, err := f.pool.Exec(ctx, "UPDATE automations SET status = 'paused' WHERE id = $1", row.ID); err != nil {
			t.Fatalf("pause automation: %v", err)
		}
		row.Status = sqlcgen.AutomationStatusPaused
	}

	return row
}

// countInvocationsForAutomation counts automation_invocations rows for
// automationID -- a small test-only helper, mirroring listRunsForInvocation's
// own identical "no store method exists for this, only assertions need
// it" shape.
func (f *testFixture) countInvocationsForAutomation(t *testing.T, automationID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM automation_invocations WHERE automation_id = $1", automationID).Scan(&count); err != nil {
		t.Fatalf("count invocations for automation: %v", err)
	}
	return count
}

func TestEvaluateCronTriggersOnce_FiresMatchingSchedule(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	auto := f.createCronAutomation(t, "every minute", "* * * * *", sqlcgen.AutomationStatusActive)

	if err := f.engine.EvaluateCronTriggersOnce(ctx); err != nil {
		t.Fatalf("EvaluateCronTriggersOnce: %v", err)
	}

	if got := f.countInvocationsForAutomation(t, auto.ID); got != 1 {
		t.Fatalf("invocations for automation = %d, want 1", got)
	}

	gotAuto, err := f.automations.Get(ctx, auto.ID)
	if err != nil {
		t.Fatalf("get automation: %v", err)
	}
	if !gotAuto.LastCronFiredAt.Valid {
		t.Fatalf("last_cron_fired_at not set after a matching tick")
	}
}

func TestEvaluateCronTriggersOnce_NeverFiresWhenScheduleDoesNotMatch(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// A minute value that can never equal time.Now().UTC().Minute() at the
	// SAME instant as an hour value that can never equal the current hour
	// either -- "at 00:00 on Feb 30" is nonsensical for day-of-month, so
	// instead this pins an hour/minute combination guaranteed to differ
	// from whatever instant the test actually runs at by construction:
	// hour 23, minute 59, AND day-of-week values excluding every day --
	// simplest reliable approach: an empty day-of-week set is impossible
	// to express in this grammar, so instead this pins minute=61-invalid?
	// No -- simplest: use a fixed, narrow single-minute/hour combination;
	// the test asserts NO invocation was created, which holds for all but
	// one exact minute of the year, and is re-run on every CI tick anyway
	// (a 1/1440 flake chance is not an acceptable risk) -- so instead this
	// test pins the schedule to a day-of-month that cannot exist this
	// month combined with an always-true minute, guaranteeing a permanent,
	// deterministic non-match: "* * 31 2 *" (Feb 31st never exists).
	auto := f.createCronAutomation(t, "impossible date", "* * 31 2 *", sqlcgen.AutomationStatusActive)

	if err := f.engine.EvaluateCronTriggersOnce(ctx); err != nil {
		t.Fatalf("EvaluateCronTriggersOnce: %v", err)
	}

	if got := f.countInvocationsForAutomation(t, auto.ID); got != 0 {
		t.Fatalf("invocations for automation = %d, want 0 for a never-matching schedule", got)
	}
}

func TestEvaluateCronTriggersOnce_NeverFiresTwiceInTheSameMinute(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	auto := f.createCronAutomation(t, "every minute", "* * * * *", sqlcgen.AutomationStatusActive)

	if err := f.engine.EvaluateCronTriggersOnce(ctx); err != nil {
		t.Fatalf("EvaluateCronTriggersOnce (first tick): %v", err)
	}
	if err := f.engine.EvaluateCronTriggersOnce(ctx); err != nil {
		t.Fatalf("EvaluateCronTriggersOnce (second tick): %v", err)
	}

	if got := f.countInvocationsForAutomation(t, auto.ID); got != 1 {
		t.Fatalf("invocations for automation = %d, want exactly 1 across two ticks in the same minute", got)
	}
}

func TestEvaluateCronTriggersOnce_SkipsPausedAutomation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	auto := f.createCronAutomation(t, "paused cron", "* * * * *", sqlcgen.AutomationStatusPaused)

	if err := f.engine.EvaluateCronTriggersOnce(ctx); err != nil {
		t.Fatalf("EvaluateCronTriggersOnce: %v", err)
	}

	if got := f.countInvocationsForAutomation(t, auto.ID); got != 0 {
		t.Fatalf("invocations for automation = %d, want 0 for a paused automation", got)
	}
}

// TestFanOut_HonorsSandboxSettings is §8.4's own "sandboxSettings honored
// on automation sessions" -- before this Step, fanout.go's own
// createRunAndSession built a bare, always-unscoped CreateSessionRequest,
// silently ignoring any sandbox scoping a maintainer configured on the
// automation itself. This drives a real PumpOnce and asserts the resulting
// run's own session carries a real environment_id whose own row reflects
// the automation's configured path scope/mock config.
func TestFanOut_HonorsSandboxSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	targets := []domainautomation.Target{{Name: "repo", URL: "https://github.com/acme/repo"}}
	reposJSON, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	pathScopeJSON, err := json.Marshal([]string{"apps/web/**"})
	if err != nil {
		t.Fatalf("marshal path scope: %v", err)
	}
	prompt := "do the thing"
	contractsPath := "contracts/custom"

	auto, err := f.automations.Create(ctx, sqlcgen.CreateAutomationParams{
		Name: "scoped automation", Prompt: &prompt, Repos: reposJSON, CreatedBy: pgtype.UUID{},
		TriggerType: sqlcgen.AutomationTriggerTypeManual, TriggerConfig: []byte("{}"), EnvVars: []byte("[]"),
		SandboxPathScope: pathScopeJSON, SandboxMockConfigured: true, SandboxContractsPath: &contractsPath,
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}

	inv := f.createInvocation(t, auto.ID, targets)
	if err := f.engine.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce: %v", err)
	}

	runs := f.listRunsForInvocation(t, inv.ID)
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	if !runs[0].SessionID.Valid {
		t.Fatalf("run has no linked session")
	}

	session, err := f.sessions.Get(ctx, runs[0].SessionID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !session.EnvironmentID.Valid {
		t.Fatalf("session has no environment_id -- sandbox settings were not honored")
	}

	env, err := f.environments.Get(ctx, session.EnvironmentID)
	if err != nil {
		t.Fatalf("get environment: %v", err)
	}
	var gotPathScope []string
	if err := json.Unmarshal(env.PathScope, &gotPathScope); err != nil {
		t.Fatalf("unmarshal environment path scope: %v", err)
	}
	if len(gotPathScope) != 1 || gotPathScope[0] != "apps/web/**" {
		t.Fatalf("environment path_scope = %v, want [apps/web/**]", gotPathScope)
	}
	if !env.MockConfigured {
		t.Fatalf("environment mock_configured = false, want true")
	}
	if env.ContractsPath == nil || *env.ContractsPath != "contracts/custom" {
		t.Fatalf("environment contracts_path = %v, want contracts/custom", env.ContractsPath)
	}
}

// TestFanOut_ThreadsEnvVarsIntoDispatchedPrompt is §8.4's own
// "per-automation env vars" -- see fanout.go's own buildRunPrompt doc
// comment for why this is surfaced via the dispatched turn's own prompt
// text rather than the sandbox process's OS environment (no generic
// per-automation/per-session env-injection mechanism into cmd.Env exists
// anywhere in this codebase yet -- Step 53's own explicit scope).
func TestFanOut_ThreadsEnvVarsIntoDispatchedPrompt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	targets := []domainautomation.Target{{Name: "repo", URL: "https://github.com/acme/repo"}}
	reposJSON, err := json.Marshal(targets)
	if err != nil {
		t.Fatalf("marshal repos: %v", err)
	}
	envVarsJSON, err := json.Marshal([]map[string]string{{"name": "TARGET_ENV", "value": "staging"}})
	if err != nil {
		t.Fatalf("marshal env vars: %v", err)
	}
	prompt := "run the audit"

	auto, err := f.automations.Create(ctx, sqlcgen.CreateAutomationParams{
		Name: "env var automation", Prompt: &prompt, Repos: reposJSON, CreatedBy: pgtype.UUID{},
		TriggerType: sqlcgen.AutomationTriggerTypeManual, TriggerConfig: []byte("{}"), EnvVars: envVarsJSON,
	})
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}

	inv := f.createInvocation(t, auto.ID, targets)
	if err := f.engine.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce: %v", err)
	}

	runs := f.listRunsForInvocation(t, inv.ID)
	if len(runs) != 1 || !runs[0].SessionID.Valid {
		t.Fatalf("expected exactly one run with a linked session")
	}

	turns, err := f.turns.ListForSession(ctx, runs[0].SessionID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("got %d turns, want 1", len(turns))
	}
	if turns[0].Prompt == nil {
		t.Fatalf("dispatched turn has no prompt")
	}
	got := *turns[0].Prompt
	if !strings.Contains(got, "TARGET_ENV=staging") || !strings.Contains(got, "run the audit") {
		t.Fatalf("dispatched prompt = %q, want it to carry both the env var preamble and the original prompt text", got)
	}
}

// TestCloseout_RecordsLastRunAndArtifactSummaryOnSuccess is §8.4's own
// "last_run + artifact_summary populated" -- success case.
func TestCloseout_RecordsLastRunAndArtifactSummaryOnSuccess(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	auto, inv, run := f.driveToOneRun(t, "closeout success test")

	f.setTurnStatus(t, run, sqlcgen.TurnStatusProcessing)
	if err := f.engine.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (promote): %v", err)
	}
	f.setTurnStatus(t, run, sqlcgen.TurnStatusCompleted)
	if err := f.engine.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce (terminalize): %v", err)
	}

	gotInv, err := f.invocations.Get(ctx, inv.ID)
	if err != nil {
		t.Fatalf("get invocation: %v", err)
	}
	if !gotInv.ClosedAt.Valid {
		t.Fatalf("invocation not closed")
	}

	gotAuto, err := f.automations.Get(ctx, auto.ID)
	if err != nil {
		t.Fatalf("get automation: %v", err)
	}
	if !gotAuto.LastRunAt.Valid {
		t.Fatalf("last_run_at not set after a closed invocation")
	}
	if gotAuto.LastRunAt.Time.Unix() != gotInv.ClosedAt.Time.Unix() {
		t.Fatalf("last_run_at = %v, want it to match the invocation's own closed_at (%v)", gotAuto.LastRunAt.Time, gotInv.ClosedAt.Time)
	}
	if gotAuto.LastRunStatus == nil || *gotAuto.LastRunStatus != sqlcgen.AutomationInvocationStatusSucceeded {
		t.Fatalf("last_run_status = %v, want succeeded", gotAuto.LastRunStatus)
	}
	if gotAuto.ArtifactSummary == nil {
		t.Fatalf("artifact_summary not set")
	}
	want := domainautomation.BuildArtifactSummary(1, 0, nil)
	if *gotAuto.ArtifactSummary != want {
		t.Fatalf("artifact_summary = %q, want %q", *gotAuto.ArtifactSummary, want)
	}
}

// TestCloseout_RecordsLastRunAndArtifactSummaryOnFailure names the failed
// target in the artifact summary -- the reason recordLastRun (closeout.go)
// reads back every run of the just-closed invocation rather than reusing
// CountTerminalForInvocation's own plain counts.
func TestCloseout_RecordsLastRunAndArtifactSummaryOnFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	auto, targets := f.createAutomation(t, "closeout failure test", 1)
	inv := f.createInvocation(t, auto.ID, targets)
	if err := f.engine.PumpOnce(ctx); err != nil {
		t.Fatalf("PumpOnce: %v", err)
	}
	runs := f.listRunsForInvocation(t, inv.ID)
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}

	f.setTurnStatus(t, runs[0], sqlcgen.TurnStatusFailed)
	if err := f.engine.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce: %v", err)
	}

	gotAuto, err := f.automations.Get(ctx, auto.ID)
	if err != nil {
		t.Fatalf("get automation: %v", err)
	}
	if gotAuto.LastRunStatus == nil || *gotAuto.LastRunStatus != sqlcgen.AutomationInvocationStatusFailed {
		t.Fatalf("last_run_status = %v, want failed", gotAuto.LastRunStatus)
	}
	if gotAuto.ArtifactSummary == nil {
		t.Fatalf("artifact_summary not set")
	}
	want := domainautomation.BuildArtifactSummary(1, 1, []string{targets[0].Name})
	if *gotAuto.ArtifactSummary != want {
		t.Fatalf("artifact_summary = %q, want %q", *gotAuto.ArtifactSummary, want)
	}
}

// TestCreateInvocation_StillWorksUnchangedForATriggerAgnosticCaller is a
// small regression guard: invocationenqueue.go's own CreateInvocation
// signature/behavior must stay exactly what it was in Step 51 -- Step
// 52's trigger evaluators (the cron pump, the webhook handler) call it
// completely unchanged, per its own doc comment's own explicit promise.
func TestCreateInvocation_StillWorksUnchangedForATriggerAgnosticCaller(t *testing.T) {
	f := newFixture(t)
	auto, targets := f.createAutomation(t, "unchanged entry point", 1)

	inv, err := automation.CreateInvocation(context.Background(), f.invocations, auto.ID, targets)
	if err != nil {
		t.Fatalf("CreateInvocation: %v", err)
	}
	if inv.AutomationID != auto.ID {
		t.Fatalf("invocation automation_id = %s, want %s", inv.AutomationID.String(), auto.ID.String())
	}
	if inv.TotalRuns != 1 {
		t.Fatalf("invocation total_runs = %d, want 1", inv.TotalRuns)
	}
}
