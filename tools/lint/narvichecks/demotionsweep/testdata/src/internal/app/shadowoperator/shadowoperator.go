package shadowoperator

import "context"

type store interface {
	UpsertLiveEgressEnabled(ctx context.Context, repo string, enabled bool) (int, error)
}

// activate stands for the real Activate path, which only ever calls this
// with true (a promotion, which owes no demotion sweep -- §30.4). It must
// NOT be reported.
func activate(ctx context.Context, s store) {
	_, _ = s.UpsertLiveEgressEnabled(ctx, "acme/widgets", true)
}
