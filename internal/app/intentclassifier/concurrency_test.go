package intentclassifier

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/sync/errgroup"

	intentdomain "github.com/khazaddev/narvi/internal/domain/intent"
)

// mutexSessionStore is a concurrency-safe DecisionStore fake that
// reproduces the guarded UPDATE's own real semantics ("UPDATE ... WHERE
// intent_decision IS NULL") under real goroutine contention: exactly one
// caller observes won=true for a given session id, no matter how many
// concurrent callers race for it -- proving the write-once contract at
// the Service.RecordDecision level (the underlying SQL guarantee itself
// is covered by migrations/000033_intent_classifier.up.sql's own guarded
// UPDATE and the postgres integration-test suite, out of this Step's own
// non-integration scope).
type mutexSessionStore struct {
	mu  sync.Mutex
	set map[pgtype.UUID]bool
}

func newMutexSessionStore() *mutexSessionStore {
	return &mutexSessionStore{set: make(map[pgtype.UUID]bool)}
}

func (m *mutexSessionStore) UpdateIntentDecisionIfNull(_ context.Context, id pgtype.UUID, _ []byte) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.set[id] {
		return false, nil
	}
	m.set[id] = true
	return true, nil
}

// TestService_RecordDecision_ConcurrentDoubleWrite_OnlyOneWins proves the
// write-once guarded UPDATE actually wins only once under a concurrent
// double-write attempt (§18.4: "first decision wins, no application-level
// lock needed") -- N goroutines race RecordDecision for the SAME session
// id; exactly one must observe won=true.
func TestService_RecordDecision_ConcurrentDoubleWrite_OnlyOneWins(t *testing.T) {
	const concurrency = 50

	sessions := newMutexSessionStore()
	svc := New(&fakeLLM{}, "anthropic", "claude-haiku-4-5", validTemplates(), sessions, nil)

	id := testSessionID()
	rec := intentdomain.IntentDecisionRecord{
		Surface:        "github",
		Source:         intentdomain.RecordSourceClassifier,
		Target:         intentdomain.TargetReview,
		Mode:           intentdomain.ModeBuild,
		DecidedAtStage: intentdomain.DecidedAtStageCreate,
	}

	results := make([]bool, concurrency)
	g, ctx := errgroup.WithContext(context.Background())
	for i := 0; i < concurrency; i++ {
		i := i
		g.Go(func() error {
			won, err := svc.RecordDecision(ctx, id, rec)
			results[i] = won
			return err
		})
	}
	if err := g.Wait(); err != nil {
		t.Fatalf("errgroup.Wait() error = %v, want nil", err)
	}

	wins := 0
	for _, won := range results {
		if won {
			wins++
		}
	}
	if wins != 1 {
		t.Errorf("wins = %d across %d concurrent RecordDecision calls, want exactly 1", wins, concurrency)
	}
}
