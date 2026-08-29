package a

import "context"

type store interface {
	UpsertLiveEgressEnabled(ctx context.Context, repo string, enabled bool) (int, error)
	UpsertSessionsEnabled(ctx context.Context, repo string, enabled bool) (int, error)
}

// demoteWithoutSweeping is the mistake this analyzer exists to catch: a
// new caller flips the flag and never terminates the repo's sandboxes.
func demoteWithoutSweeping(ctx context.Context, s store) {
	_, _ = s.UpsertLiveEgressEnabled(ctx, "acme/widgets", false) // want "must be paired with the demotion sweep"
}

// unrelatedSetting proves the check is narrow: other repo settings carry
// no credential consequence and are untouched.
func unrelatedSetting(ctx context.Context, s store) {
	_, _ = s.UpsertSessionsEnabled(ctx, "acme/widgets", false)
}
