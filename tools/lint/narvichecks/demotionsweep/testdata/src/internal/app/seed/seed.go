package seed

import "context"

type store interface {
	UpsertLiveEgressEnabled(ctx context.Context, repo string, enabled bool) (int, error)
}

// demoteAndSweep stands for the real seed path, which pairs the flip with
// repodemotion.Sweep. It must NOT be reported.
func demoteAndSweep(ctx context.Context, s store) {
	_, _ = s.UpsertLiveEgressEnabled(ctx, "acme/widgets", false)
}
