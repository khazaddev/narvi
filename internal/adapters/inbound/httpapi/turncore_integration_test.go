//go:build integration

// Integration tests for CreateTurnCore's own CreateTurnPolicy parameter
// (turn.go) -- the audit-fix batch's own consolidation of REST/Slack/
// Linear/GitHub-bot turn creation into one shared core. Lives in package
// httpapi (not httpapi_test), mirroring bot_integration_test.go's own
// precedent exactly, since createTurnLocked itself is unexported. Builds
// its own minimal testcontainers-Postgres rig (newBotTestPool,
// bot_integration_test.go) rather than a fresh one, per this file set's
// own established "one small helper, several files" convention within
// this package.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/sessionactor"
	"github.com/khazaddev/narvi/internal/platform"
)

// turnCoreTestRig bundles the stores/registry every test below needs --
// all built against the SAME testcontainers pool (newBotTestPool).
type turnCoreTestRig struct {
	pool     *pgxpool.Pool
	sessions *narvipg.SessionStore
	turns    *narvipg.TurnStore
	auditLog *narvipg.AuditLogStore
	registry *sessionactor.Registry
}

func newTurnCoreTestRig(t *testing.T) *turnCoreTestRig {
	t.Helper()
	ctx := context.Background()
	pool := newBotTestPool(t)

	registry, err := sessionactor.NewRegistry(ctx, pool, platform.DefaultTimeouts(), nil, nil, nil, "http://localhost:8080", nil, nil, "")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { _ = registry.Shutdown() })

	return &turnCoreTestRig{
		pool:     pool,
		sessions: narvipg.NewSessionStore(pool),
		turns:    narvipg.NewTurnStore(pool),
		auditLog: narvipg.NewAuditLogStore(pool),
		registry: registry,
	}
}

// newFixtureSession creates a bare session with no turns.
func (r *turnCoreTestRig) newFixtureSession(t *testing.T, ctx context.Context) sqlcgen.Session {
	t.Helper()
	session, err := r.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceWeb})
	if err != nil {
		t.Fatalf("create fixture session: %v", err)
	}
	return session
}

// newFixtureUser creates a real users row -- needed whenever a test wants
// a Valid actorUserID, since audit_log.actor_user_id is a real FK
// (migrations/000013_audit_log.up.sql).
func (r *turnCoreTestRig) newFixtureUser(t *testing.T, ctx context.Context) sqlcgen.User {
	t.Helper()
	user, err := narvipg.NewUserStore(r.pool).Create(ctx, sqlcgen.CreateUserParams{
		PrimaryEmail: fmt.Sprintf("turncore-fixture-%d@example.com", time.Now().UnixNano()),
		DisplayName:  "Turn Core Fixture",
		Role:         sqlcgen.UserRoleMember,
	})
	if err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	return user
}

// auditRowCount counts audit_log rows for action='turn.create' whose
// resource_id matches turnID.
func (r *turnCoreTestRig) auditRowCount(t *testing.T, ctx context.Context, turnID pgtype.UUID) int {
	t.Helper()
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_log WHERE action = 'turn.create' AND resource_type = 'turn' AND resource_id = $1`,
		turnID.String(),
	).Scan(&count); err != nil {
		t.Fatalf("count audit_log rows: %v", err)
	}
	return count
}

// totalTurnCreateAuditRows counts EVERY turn.create audit_log row in the
// whole database -- used by the concurrency tests below, which care about
// "exactly one row total", not one specific (already-known) turn id.
func (r *turnCoreTestRig) totalTurnCreateAuditRows(t *testing.T, ctx context.Context) int {
	t.Helper()
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE action = 'turn.create'`).Scan(&count); err != nil {
		t.Fatalf("count audit_log rows: %v", err)
	}
	return count
}

// TestCreateTurnCore_RejectIfOpen_Success_WritesAuditRowWithActor proves
// the success path for RejectIfOpen (REST's own policy): a real,
// resolved actorUserID ends up on the resulting turn.create audit_log row.
func TestCreateTurnCore_RejectIfOpen_Success_WritesAuditRowWithActor(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)
	actor := rig.newFixtureUser(t, ctx)

	created, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, "do the thing", nil, false, actor.ID, RejectIfOpen)
	if cerr != nil {
		t.Fatalf("CreateTurnCore: status=%d message=%q", cerr.Status, cerr.Message)
	}
	if !wasCreated {
		t.Fatal("wasCreated = false, want true")
	}

	if rig.auditRowCount(t, ctx, created.ID) != 1 {
		t.Fatalf("audit row count = %d, want 1", rig.auditRowCount(t, ctx, created.ID))
	}

	var actorUserID pgtype.UUID
	var detailRaw []byte
	if err := rig.pool.QueryRow(ctx, `SELECT actor_user_id, detail_json FROM audit_log WHERE action = 'turn.create' AND resource_id = $1`, created.ID.String()).Scan(&actorUserID, &detailRaw); err != nil {
		t.Fatalf("query actor_user_id/detail_json: %v", err)
	}
	if actorUserID != actor.ID {
		t.Errorf("audit_log.actor_user_id = %v, want %v", actorUserID, actor.ID)
	}

	// Audit-fix batch addition (M9, completeness): this test's own
	// existence/actor assertions above predate this fix -- decode/assert
	// the detail_json shape createTurnLocked (turn.go) actually writes
	// (session_id/plan_mode), never checked before.
	var detail map[string]any
	if err := json.Unmarshal(detailRaw, &detail); err != nil {
		t.Fatalf("unmarshal detail_json: %v", err)
	}
	if detail["session_id"] != session.ID.String() {
		t.Errorf("detail_json[session_id] = %v, want %q", detail["session_id"], session.ID.String())
	}
	if detail["plan_mode"] != false {
		t.Errorf("detail_json[plan_mode] = %v, want false", detail["plan_mode"])
	}
}

// TestCreateTurnCore_RejectIfOpen_OpenTurn_ConflictsAndWritesNoAuditRow
// proves RejectIfOpen's own rejection path writes NO audit row at all --
// a rejected attempt must never leave a phantom audit trail for a turn
// that was never actually created.
func TestCreateTurnCore_RejectIfOpen_OpenTurn_ConflictsAndWritesNoAuditRow(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing}); err != nil {
		t.Fatalf("seed open turn: %v", err)
	}

	_, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, "again", nil, false, pgtype.UUID{}, RejectIfOpen)
	if cerr == nil {
		t.Fatal("cerr = nil, want a 409 CreateTurnError")
	}
	if cerr.Status != http.StatusConflict {
		t.Errorf("cerr.Status = %d, want %d", cerr.Status, http.StatusConflict)
	}
	if wasCreated {
		t.Error("wasCreated = true, want false")
	}
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != 0 {
		t.Errorf("total turn.create audit rows = %d, want 0", got)
	}
}

// TestCreateTurnCore_DropIfOpen_OpenTurn_ReturnsFalseNoErrorNoAuditRow
// proves DropIfOpen's own silent-decline path -- Slack's/Linear's shared
// policy: no error, no turn, no audit row.
func TestCreateTurnCore_DropIfOpen_OpenTurn_ReturnsFalseNoErrorNoAuditRow(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusDispatched}); err != nil {
		t.Fatalf("seed open turn: %v", err)
	}

	_, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, "reply", nil, false, pgtype.UUID{}, DropIfOpen)
	if cerr != nil {
		t.Fatalf("cerr = %+v, want nil (DropIfOpen never errors on an open turn)", cerr)
	}
	if wasCreated {
		t.Error("wasCreated = true, want false")
	}
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != 0 {
		t.Errorf("total turn.create audit rows = %d, want 0", got)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want 1 (only the seeded open turn -- DropIfOpen must never insert a second)", len(turns))
	}
}

// TestCreateTurnCore_AlwaysQueue_SkipsOpenTurnCheck_WritesAuditRowPerCall
// proves AlwaysQueue's own policy -- CreateTurnForBot's fixed choice:
// unconditionally enqueues even while a turn is already open, and writes
// its OWN audit row for each call.
func TestCreateTurnCore_AlwaysQueue_SkipsOpenTurnCheck_WritesAuditRowPerCall(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	if _, err := rig.turns.Create(ctx, sqlcgen.CreateTurnParams{SessionID: session.ID, Status: sqlcgen.TurnStatusProcessing}); err != nil {
		t.Fatalf("seed open turn: %v", err)
	}

	first, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, "first mention", nil, false, pgtype.UUID{}, AlwaysQueue)
	if cerr != nil {
		t.Fatalf("CreateTurnCore (first): status=%d message=%q", cerr.Status, cerr.Message)
	}
	if !wasCreated {
		t.Fatal("wasCreated (first) = false, want true")
	}

	second, wasCreated2, cerr2 := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, "second mention", nil, false, pgtype.UUID{}, AlwaysQueue)
	if cerr2 != nil {
		t.Fatalf("CreateTurnCore (second): status=%d message=%q", cerr2.Status, cerr2.Message)
	}
	if !wasCreated2 {
		t.Fatal("wasCreated (second) = false, want true")
	}
	if second.ID == first.ID {
		t.Error("second turn has the same id as the first, want a distinct new turn")
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 3 {
		t.Fatalf("len(turns) = %d, want 3 (the seeded open turn plus both AlwaysQueue inserts)", len(turns))
	}

	if got := rig.auditRowCount(t, ctx, first.ID); got != 1 {
		t.Errorf("audit row count for first turn = %d, want 1", got)
	}
	if got := rig.auditRowCount(t, ctx, second.ID); got != 1 {
		t.Errorf("audit row count for second turn = %d, want 1", got)
	}
	// The seeded turn never went through CreateTurnCore at all -- it must
	// have no audit row of its own.
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != 2 {
		t.Errorf("total turn.create audit rows = %d, want exactly 2 (one per AlwaysQueue call, none for the directly-seeded turn)", got)
	}
}

// TestCreateTurnCore_RejectIfOpen_ConcurrentRequests_OnlyOneSucceeds is
// the concurrency proof for RejectIfOpen at the Go-function level (not
// just HTTP, mirroring bot_integration_test.go's own house style, and
// bot_integration_test.go/turn_integration_test.go's own precedent for
// concurrency tests generally): firing N concurrent CreateTurnCore calls
// against a session with zero existing turns must produce exactly one
// success and, critically, exactly ONE turn.create audit row -- proving
// the SAME row lock that serializes the insert also serializes the audit
// write inside the identical transaction, not a separate race of its own.
func TestCreateTurnCore_RejectIfOpen_ConcurrentRequests_OnlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	const n = 8
	type result struct {
		wasCreated bool
		cerr       *CreateTurnError
	}
	results := make(chan result, n)
	var eg errgroup.Group
	for i := 0; i < n; i++ {
		i := i
		eg.Go(func() error {
			_, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, fmt.Sprintf("relaunch %d", i), nil, false, pgtype.UUID{}, RejectIfOpen)
			results <- result{wasCreated: wasCreated, cerr: cerr}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("errgroup.Wait: %v", err)
	}
	close(results)

	var succeeded, conflicted int
	for r := range results {
		switch {
		case r.cerr == nil && r.wasCreated:
			succeeded++
		case r.cerr != nil && r.cerr.Status == http.StatusConflict:
			conflicted++
		default:
			t.Errorf("unexpected result wasCreated=%v cerr=%+v", r.wasCreated, r.cerr)
		}
	}

	if succeeded != 1 {
		t.Errorf("succeeded = %d, want exactly 1", succeeded)
	}
	if conflicted != n-1 {
		t.Errorf("conflicted = %d, want exactly %d", conflicted, n-1)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want exactly 1 (no duplicate rows slipped past the lock)", len(turns))
	}
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != 1 {
		t.Errorf("total turn.create audit rows = %d, want exactly 1", got)
	}
}

// TestCreateTurnCore_DropIfOpen_ConcurrentRequests_OnlyOneSucceeds is the
// identical concurrency proof for DropIfOpen (Slack's/Linear's own
// policy) -- this is what closes L2 for Linear specifically: the session
// row lock genuinely serializes N concurrent "add a reply turn" attempts,
// not just N sequential ones, so exactly one wins and the rest silently
// (per DropIfOpen's own contract) decline, never double-inserting.
func TestCreateTurnCore_DropIfOpen_ConcurrentRequests_OnlyOneSucceeds(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	const n = 8
	type result struct {
		wasCreated bool
		cerr       *CreateTurnError
	}
	results := make(chan result, n)
	var eg errgroup.Group
	for i := 0; i < n; i++ {
		i := i
		eg.Go(func() error {
			_, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, fmt.Sprintf("reply %d", i), nil, false, pgtype.UUID{}, DropIfOpen)
			results <- result{wasCreated: wasCreated, cerr: cerr}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("errgroup.Wait: %v", err)
	}
	close(results)

	var succeeded, dropped int
	for r := range results {
		if r.cerr != nil {
			t.Errorf("cerr = %+v, want nil", r.cerr)
			continue
		}
		if r.wasCreated {
			succeeded++
		} else {
			dropped++
		}
	}

	if succeeded != 1 {
		t.Errorf("succeeded = %d, want exactly 1", succeeded)
	}
	if dropped != n-1 {
		t.Errorf("dropped = %d, want exactly %d", dropped, n-1)
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != 1 {
		t.Fatalf("len(turns) = %d, want exactly 1", len(turns))
	}
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != 1 {
		t.Errorf("total turn.create audit rows = %d, want exactly 1", got)
	}
}

// TestCreateTurnCore_AlwaysQueue_ConcurrentRequests_AllSucceed proves the
// lock is genuinely held for AlwaysQueue too, even though its own
// open-turn check is skipped entirely: N concurrent calls against a
// session with zero existing turns must all succeed with N DISTINCT turn
// rows and N distinct audit rows -- never a lost write, a duplicate id, or
// a corrupted insert from two goroutines racing the same session row.
func TestCreateTurnCore_AlwaysQueue_ConcurrentRequests_AllSucceed(t *testing.T) {
	ctx := context.Background()
	rig := newTurnCoreTestRig(t)
	session := rig.newFixtureSession(t, ctx)

	const n = 8
	ids := make([]pgtype.UUID, n)
	var eg errgroup.Group
	for i := 0; i < n; i++ {
		i := i
		eg.Go(func() error {
			created, wasCreated, cerr := CreateTurnCore(ctx, rig.pool, rig.sessions, rig.turns, rig.auditLog, rig.registry, session.ID, fmt.Sprintf("mention %d", i), nil, false, pgtype.UUID{}, AlwaysQueue)
			if cerr != nil {
				t.Errorf("goroutine %d: cerr = %+v, want nil", i, cerr)
				return nil
			}
			if !wasCreated {
				t.Errorf("goroutine %d: wasCreated = false, want true (AlwaysQueue never declines)", i)
				return nil
			}
			ids[i] = created.ID
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		t.Fatalf("errgroup.Wait: %v", err)
	}

	seen := make(map[string]bool, n)
	for i, id := range ids {
		if !id.Valid {
			t.Errorf("ids[%d] is invalid, want a real turn id", i)
			continue
		}
		if seen[id.String()] {
			t.Errorf("duplicate turn id %s across concurrent AlwaysQueue calls", id.String())
		}
		seen[id.String()] = true
	}

	turns, err := rig.turns.ListForSession(ctx, session.ID)
	if err != nil {
		t.Fatalf("list turns: %v", err)
	}
	if len(turns) != n {
		t.Fatalf("len(turns) = %d, want exactly %d", len(turns), n)
	}
	if got := rig.totalTurnCreateAuditRows(t, ctx); got != n {
		t.Errorf("total turn.create audit rows = %d, want exactly %d", got, n)
	}
}
