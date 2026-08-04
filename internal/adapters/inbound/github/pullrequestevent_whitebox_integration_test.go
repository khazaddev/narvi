//go:build integration

// White-box (package github, not github_test) integration tests covering
// handlePullRequestClosed's own decision.Allowed == true branch (the
// CherryPickAndMerge call, and the MarkMerged-vs-mergeErrString split) --
// a confirmed re-review finding found this branch completely unexercised
// by any test, in EITHER direction (real or fake): production wiring
// (handler.go) always constructs githubMergeGateDataSource (whose
// CIStatus/MergeableCleanly unconditionally error) and
// notImplementedFixMerger (whose CherryPickAndMerge unconditionally
// errors), so decision.Allowed can never be true through the real HTTP
// handler; and pullrequestevent_integration_test.go's own three tests
// (package github_test, external) can only ever drive the handler through
// that same real wiring, structurally unable to inject a fake
// mergeGateDataSource/fixMerger to reach the true branch at all.
//
// This file calls handlePullRequestClosed DIRECTLY (never through an HTTP
// server) with fake mergeGateDataSource/fixMerger implementations --
// possible only from package github itself, since both types and the
// function itself are unexported -- needing a real Postgres pool only
// because sentinelFixes/repoSettings/auditLog are concrete
// *postgres.Store types, not interfaces (mirroring
// internal/app/imagebuild's own established "duplicate newTestPool for
// this cross-package reason" precedent, builder_whitebox_integration_test.
// go).
//
// Also covers the confirmed "low" finding this same file's own production
// code (pullrequestevent.go) fixes: stackRegistered must be resolved
// FRESH via mergeGateDataSource.StackRegistered, never read off the
// persisted sentinel_fixes.stack_registered column -- proven here by
// seeding the persisted column to one value and configuring the fake
// data source to return the OPPOSITE value, then asserting fixMerger
// received the fake's (fresh) value.
package github

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepg "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/migrations"
)

// newPullRequestEventWhiteboxTestPool duplicates handler_integration_test.
// go's own newTestPool -- deliberately: that helper is unexported in a
// DIFFERENT test package (github_test), so this white-box package
// (github) cannot import it, mirroring internal/app/imagebuild's own
// newWhiteboxTestPool precedent exactly (its own doc comment gives the
// full reasoning).
func newPullRequestEventWhiteboxTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	// startCtx bounds the container-startup call below via the ambient
	// context (image pull + Docker daemon round trip + Postgres's own
	// internal ready-wait) -- kept as defense in depth, but NOT solely
	// relied upon any more: CI run 30834918806 showed this exact bound
	// (added after CI run 30831633470's own ContainerStart hang) itself
	// fail to actually cut the call off when the hang recurred one layer
	// deeper, inside testcontainers-go's own wait.(*LogStrategy).
	// WaitUntilReady -- the goroutine dump showed it looping on a 100ms
	// poll for the FULL 10-minute panic window, never once observing
	// ctx.Done(), despite this same context chain being correctly wired
	// all the way through (confirmed directly: reproducing an
	// impossible-to-satisfy wait condition locally against this exact
	// call DOES correctly time out via this same context mechanism, at
	// testcontainers' own hardcoded 60s deadline -- so the mechanism is
	// sound in isolation, but evidently not dependable against whatever a
	// genuinely stalled CI-runner Docker daemon does to it in practice).
	//
	// Rather than keep chasing exactly why context cancellation isn't
	// always honored deep inside a third-party library under conditions
	// this dev machine cannot reproduce, the startup call now ALSO runs on
	// its own goroutine (via errgroup.Group.Go -- no naked `go` statement,
	// §11) raced against an independent, plain time.After watchdog:
	// whichever of "the call returned" or "the watchdog fired" happens
	// first decides the outcome, with no dependency on any context
	// cancellation actually being honored by anything downstream. If the
	// watchdog wins, the goroutine is deliberately abandoned (leaked, not
	// joined) rather than blocking this test's own cleanup on a call that
	// has already demonstrated it can ignore its own cancellation signal.
	startCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	const containerStartWatchdog = 2*time.Minute + 15*time.Second
	type containerStartResult struct {
		container *tcpostgres.PostgresContainer
		err       error
	}
	startCh := make(chan containerStartResult, 1)
	var startGroup errgroup.Group
	startGroup.Go(func() error {
		container, err := tcpostgres.Run(startCtx, "postgres:17-alpine",
			tcpostgres.WithDatabase("narvi_test"),
			tcpostgres.WithUsername("narvi"),
			tcpostgres.WithPassword("narvi"),
			tcpostgres.BasicWaitStrategies(),
		)
		startCh <- containerStartResult{container: container, err: err}
		return nil
	})

	var container *tcpostgres.PostgresContainer
	var err error
	select {
	case res := <-startCh:
		container, err = res.container, res.err
		if err != nil {
			t.Fatalf("start postgres container: %v", err)
		}
	case <-time.After(containerStartWatchdog):
		t.Fatalf("start postgres container: tcpostgres.Run did not return within %s -- Docker daemon likely "+
			"stalled without honoring context cancellation (see this function's own doc comment)", containerStartWatchdog)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Errorf("terminate container: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	migrateDB, err := sql.Open("pgx", connStr)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = migrateDB.Close() })

	dbDriver, err := migratepg.WithInstance(migrateDB, &migratepg.Config{})
	if err != nil {
		t.Fatalf("migratepg.WithInstance: %v", err)
	}
	srcDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("iofs.New: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", srcDriver, "pgx", dbDriver)
	if err != nil {
		t.Fatalf("migrate.NewWithInstance: %v", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := narvipg.NewPool(ctx, connStr)
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}

// fakeMergeGateDataSource is a test-only mergeGateDataSource -- every
// method returns a caller-configured, fixed value, letting these tests
// drive EvaluateMergeGate to Allowed=true deterministically (something
// githubMergeGateDataSource, this package's one real implementation, can
// never do today: CIStatus/MergeableCleanly always error).
type fakeMergeGateDataSource struct {
	changedFiles    []string
	changedFilesErr error
	ciGreen         bool
	ciErr           error
	mergeableClean  bool
	mergeableErr    error
	stackRegistered bool
	stackErr        error
}

func (f *fakeMergeGateDataSource) ChangedFiles(context.Context, string, string, int32) ([]string, error) {
	return f.changedFiles, f.changedFilesErr
}

func (f *fakeMergeGateDataSource) CIStatus(context.Context, string, string, int32) (bool, error) {
	return f.ciGreen, f.ciErr
}

func (f *fakeMergeGateDataSource) MergeableCleanly(context.Context, string, string, int32) (bool, error) {
	return f.mergeableClean, f.mergeableErr
}

func (f *fakeMergeGateDataSource) StackRegistered(context.Context, string, string, int32) (bool, error) {
	return f.stackRegistered, f.stackErr
}

var _ mergeGateDataSource = (*fakeMergeGateDataSource)(nil)

// cherryPickCall records one fakeFixMerger.CherryPickAndMerge invocation.
type cherryPickCall struct {
	owner, repo     string
	fixPRNumber     int
	stackRegistered bool
}

// fakeFixMerger is a test-only fixMerger -- records every call it
// receives and returns a caller-configured error, the polar opposite of
// notImplementedFixMerger (this package's one real, always-denying
// implementation).
type fakeFixMerger struct {
	mu    sync.Mutex
	calls []cherryPickCall
	err   error
}

func (f *fakeFixMerger) CherryPickAndMerge(_ context.Context, owner, repo string, fixPRNumber int, stackRegistered bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cherryPickCall{owner: owner, repo: repo, fixPRNumber: fixPRNumber, stackRegistered: stackRegistered})
	return f.err
}

func (f *fakeFixMerger) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeFixMerger) lastCall() cherryPickCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

var _ fixMerger = (*fakeFixMerger)(nil)

// wholeAllowedFixture seeds a fully-opened sentinel_fixes row (status
// fix_open, a real fix PR number) plus an enabled repo_settings row --
// exactly the precondition handlePullRequestClosed needs to even reach
// its own decision.Allowed branch at all.
func wholeAllowedFixtureForTest(ctx context.Context, t *testing.T, pool *pgxpool.Pool, repoFullName string, originPRNumber, fixPRNumber int32, persistedStackRegistered bool) sqlcgen.SentinelFix {
	t.Helper()

	sessions := narvipg.NewSessionStore(pool)
	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)

	originSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create origin session: %v", err)
	}
	childSession, err := sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub})
	if err != nil {
		t.Fatalf("create child session: %v", err)
	}

	fix, err := sentinelFixes.Claim(ctx, repoFullName, originPRNumber, originSession.ID, "feature-x")
	if err != nil {
		t.Fatalf("claim sentinel_fixes: %v", err)
	}
	if _, err := sentinelFixes.UpdateChildSession(ctx, fix.ID, childSession.ID); err != nil {
		t.Fatalf("update child session: %v", err)
	}
	if _, err := sentinelFixes.UpdateOpened(ctx, fix.ID, fixPRNumber); err != nil {
		t.Fatalf("update opened: %v", err)
	}
	if _, err := sentinelFixes.UpdateStackRegistered(ctx, fix.ID, persistedStackRegistered); err != nil {
		t.Fatalf("update stack registered: %v", err)
	}
	if _, err := repoSettings.Upsert(ctx, repoFullName, false, true); err != nil {
		t.Fatalf("upsert repo settings (sentinel_autofix_enabled=true): %v", err)
	}

	got, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	return got
}

// pullRequestClosedBodyForTest builds a real-shaped `pull_request` webhook
// payload with action=="closed" and the given merged value -- mirrors
// pullrequestevent_integration_test.go's own identical pullRequestClosedBody
// helper (that one lives in package github_test, not visible here).
func pullRequestClosedBodyForTest(t *testing.T, repoFullName string, prNumber int, merged bool) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"action": "closed",
		"repository": map[string]any{
			"full_name": repoFullName,
		},
		"pull_request": map[string]any{
			"number": prNumber,
			"merged": merged,
		},
	})
	if err != nil {
		t.Fatalf("marshal pull_request closed body: %v", err)
	}
	return body
}

// TestHandlePullRequestClosed_MergeAllowed_Succeeds_MarksMerged proves the
// confirmed re-review finding's first half: given a fake mergeGateDataSource/
// fixMerger that make decision.Allowed reachably true, a successful
// CherryPickAndMerge call actually marks the sentinel_fixes row
// fix_merged and records an audit_log row with merge_attempted=true.
func TestHandlePullRequestClosed_MergeAllowed_Succeeds_MarksMerged(t *testing.T) {
	ctx := context.Background()
	pool := newPullRequestEventWhiteboxTestPool(t)

	repoFullName := "acme/whitebox-merge-succeeds"
	fix := wholeAllowedFixtureForTest(ctx, t, pool, repoFullName, 300, 301, false)

	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	dataSource := &fakeMergeGateDataSource{
		changedFiles:    []string{"foo_test.go"},
		ciGreen:         true,
		mergeableClean:  true,
		stackRegistered: true,
	}
	merger := &fakeFixMerger{}

	w := httptest.NewRecorder()
	handlePullRequestClosed(ctx, w, pullRequestClosedBodyForTest(t, repoFullName, 300, true), sentinelFixes, repoSettings, auditLog, dataSource, merger)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	if merger.callCount() != 1 {
		t.Fatalf("CherryPickAndMerge called %d times, want exactly 1 -- this is the confirmed finding's own central gap: this branch was never reachable by any prior test", merger.callCount())
	}
	call := merger.lastCall()
	if call.fixPRNumber != 301 {
		t.Errorf("CherryPickAndMerge fixPRNumber = %d, want 301", call.fixPRNumber)
	}

	got, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if got.Status != "fix_merged" {
		t.Errorf("Status = %q, want %q (a successful CherryPickAndMerge must actually mark the row merged)", got.Status, "fix_merged")
	}

	var mergeAttempted, allowed bool
	if err := pool.QueryRow(ctx,
		`SELECT (detail_json->>'merge_attempted')::boolean, (detail_json->>'allowed')::boolean FROM audit_log WHERE resource_type = 'sentinel_fix' AND resource_id = $1`,
		fix.ID.String(),
	).Scan(&mergeAttempted, &allowed); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if !allowed || !mergeAttempted {
		t.Errorf("audit_log allowed/merge_attempted = %v/%v, want true/true", allowed, mergeAttempted)
	}
}

// TestHandlePullRequestClosed_MergeAllowed_CherryPickFails_RecordsErrorNeverMerges
// proves the confirmed finding's second half: when CherryPickAndMerge
// itself fails, the sentinel_fixes row must NEVER be marked merged, and
// the failure reason must be recorded in the audit_log row's own
// merge_error field.
func TestHandlePullRequestClosed_MergeAllowed_CherryPickFails_RecordsErrorNeverMerges(t *testing.T) {
	ctx := context.Background()
	pool := newPullRequestEventWhiteboxTestPool(t)

	repoFullName := "acme/whitebox-merge-fails"
	fix := wholeAllowedFixtureForTest(ctx, t, pool, repoFullName, 400, 401, false)

	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	dataSource := &fakeMergeGateDataSource{
		changedFiles:   []string{"foo_test.go"},
		ciGreen:        true,
		mergeableClean: true,
	}
	wantErr := errors.New("simulated cherry-pick conflict")
	merger := &fakeFixMerger{err: wantErr}

	w := httptest.NewRecorder()
	handlePullRequestClosed(ctx, w, pullRequestClosedBodyForTest(t, repoFullName, 400, true), sentinelFixes, repoSettings, auditLog, dataSource, merger)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if merger.callCount() != 1 {
		t.Fatalf("CherryPickAndMerge called %d times, want exactly 1", merger.callCount())
	}

	got, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if got.Status == "fix_merged" {
		t.Errorf("Status = %q, want it NEVER marked merged when CherryPickAndMerge itself failed", got.Status)
	}

	var mergeError string
	if err := pool.QueryRow(ctx,
		`SELECT detail_json->>'merge_error' FROM audit_log WHERE resource_type = 'sentinel_fix' AND resource_id = $1`,
		fix.ID.String(),
	).Scan(&mergeError); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if mergeError != wantErr.Error() {
		t.Errorf("audit_log merge_error = %q, want %q", mergeError, wantErr.Error())
	}
}

// TestHandlePullRequestClosed_UsesFreshStackRegistered_NeverPersistedColumn
// is the confirmed "low" finding's own regression test: sentinel_fixes.
// stack_registered is seeded to TRUE, but the fake mergeGateDataSource's
// own StackRegistered reports FALSE (simulating "registration silently
// stopped sticking since it was recorded") -- CherryPickAndMerge must
// receive the FRESH (false), never the persisted (true), value.
func TestHandlePullRequestClosed_UsesFreshStackRegistered_NeverPersistedColumn(t *testing.T) {
	ctx := context.Background()
	pool := newPullRequestEventWhiteboxTestPool(t)

	repoFullName := "acme/whitebox-fresh-stack"
	// Persisted column: TRUE.
	wholeAllowedFixtureForTest(ctx, t, pool, repoFullName, 500, 501, true)

	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	// Fake (fresh) data source: FALSE -- the opposite of the persisted
	// column, so this test fails loudly if the caller ever falls back to
	// reading fix.StackRegistered instead of calling this method.
	dataSource := &fakeMergeGateDataSource{
		changedFiles:   []string{"foo_test.go"},
		ciGreen:        true,
		mergeableClean: true,
		// stackRegistered zero-valued: false.
	}
	merger := &fakeFixMerger{}

	w := httptest.NewRecorder()
	handlePullRequestClosed(ctx, w, pullRequestClosedBodyForTest(t, repoFullName, 500, true), sentinelFixes, repoSettings, auditLog, dataSource, merger)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if merger.callCount() != 1 {
		t.Fatalf("CherryPickAndMerge called %d times, want exactly 1", merger.callCount())
	}
	if got := merger.lastCall().stackRegistered; got != false {
		t.Errorf("CherryPickAndMerge stackRegistered = %v, want false (the FRESH mergeGateDataSource.StackRegistered value, never the persisted true column)", got)
	}
}

// TestHandlePullRequestClosed_StackRegisteredCheckFails_NeverMerges proves
// a failure to determine the fresh stack-registration state is treated
// exactly like any other CherryPickAndMerge failure: no merge is
// attempted at all, and the reason is recorded.
func TestHandlePullRequestClosed_StackRegisteredCheckFails_NeverMerges(t *testing.T) {
	ctx := context.Background()
	pool := newPullRequestEventWhiteboxTestPool(t)

	repoFullName := "acme/whitebox-stack-check-fails"
	fix := wholeAllowedFixtureForTest(ctx, t, pool, repoFullName, 600, 601, false)

	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	dataSource := &fakeMergeGateDataSource{
		changedFiles:   []string{"foo_test.go"},
		ciGreen:        true,
		mergeableClean: true,
		stackErr:       errors.New("simulated GetPullRequest failure"),
	}
	merger := &fakeFixMerger{}

	w := httptest.NewRecorder()
	handlePullRequestClosed(ctx, w, pullRequestClosedBodyForTest(t, repoFullName, 600, true), sentinelFixes, repoSettings, auditLog, dataSource, merger)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if merger.callCount() != 0 {
		t.Errorf("CherryPickAndMerge called %d times, want 0 (a failed fresh stack-registration check must never fall through to a real merge attempt)", merger.callCount())
	}

	got, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if got.Status == "fix_merged" {
		t.Errorf("Status = %q, want it never merged", got.Status)
	}
}

// TestHandlePullRequestClosed_ChangedFilesBlindToRename_DeniesViaOldPath is
// an end-to-end regression test for the confirmed "high" rename-blindness
// finding: ChangedFiles here returns BOTH the rename's old (real,
// non-test) and new (test-looking) path -- exactly what
// parseChangedFilesFromDiff now reports for a real rename diff (see
// pullrequestevent_test.go's own unit tests for that parser in
// isolation) -- and EvaluateMergeGate must deny on the old path, never
// merging.
func TestHandlePullRequestClosed_ChangedFilesBlindToRename_DeniesViaOldPath(t *testing.T) {
	ctx := context.Background()
	pool := newPullRequestEventWhiteboxTestPool(t)

	repoFullName := "acme/whitebox-rename-attack"
	fix := wholeAllowedFixtureForTest(ctx, t, pool, repoFullName, 700, 701, false)

	sentinelFixes := narvipg.NewSentinelFixStore(pool)
	repoSettings := narvipg.NewRepoSettingsStore(pool)
	auditLog := narvipg.NewAuditLogStore(pool)

	dataSource := &fakeMergeGateDataSource{
		// The exact real-world shape parseChangedFilesFromDiff now returns
		// for `git mv real_impl.go real_impl_test.go` with zero content
		// change: BOTH the old (real) and new (test-looking) path.
		changedFiles:   []string{"internal/foo/real_impl.go", "internal/foo/real_impl_test.go"},
		ciGreen:        true,
		mergeableClean: true,
	}
	merger := &fakeFixMerger{}

	w := httptest.NewRecorder()
	handlePullRequestClosed(ctx, w, pullRequestClosedBodyForTest(t, repoFullName, 700, true), sentinelFixes, repoSettings, auditLog, dataSource, merger)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if merger.callCount() != 0 {
		t.Errorf("CherryPickAndMerge called %d times, want 0 -- the renamed-away real production file must deny the merge", merger.callCount())
	}

	got, err := sentinelFixes.GetByID(ctx, fix.ID)
	if err != nil {
		t.Fatalf("get sentinel_fixes: %v", err)
	}
	if got.Status == "fix_merged" {
		t.Error("Status = fix_merged, want it denied (a renamed-away real production file must never slip through as \"every file is test/doc\")")
	}
}
