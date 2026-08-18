package ports

// SentinelAutoFixPayload is NotificationKindSentinelAutoFix's own outbox
// payload shape (Step 48, "sentinels + suggestions", §17.2) --
// constructed by internal/adapters/inbound/httpapi/reviewverdict.go (the
// only writer, inside the SAME transaction as the triggering verdict's
// own findings-upsert/verdict-outbox-enqueue write) and consumed by
// internal/app/outboxworker's own sentinel-auto-fix notifier (the only
// reader).
//
// Defined HERE, in ports, specifically because those two packages cannot
// import each other directly without a cycle: the notifier must import
// httpapi (to call its exported session-creation machinery -- today,
// CreateSessionOnTx/TriggerDispatch directly, mirroring internal/adapters/
// inbound/github's own coalesce.go "already callable from outside httpapi
// by design" precedent), so httpapi cannot import outboxworker back for
// this one type. ports is already imported by both, imports neither -- the
// same arm's-length layer every other Notification's own Kind/Payload
// pairing already lives at (notifier.go).
type SentinelAutoFixPayload struct {
	// SentinelFixID is the sentinel_fixes row's own id (already claimed,
	// same transaction, by reviewverdict.go's own call to
	// SentinelFixStore.Claim) -- the notifier's own idempotency check
	// (has this row already been spawned for) keys off this, not off the
	// outbox row's own id, so a redelivered/retried outbox entry can never
	// double-spawn a child session for the SAME claim.
	SentinelFixID string `json:"sentinelFixId"`
	// RepoFullName/OriginPRNumber identify the origin PR.
	RepoFullName   string `json:"repoFullName"`
	OriginPRNumber int32  `json:"originPrNumber"`
	// OriginReviewSessionID is the review session whose posted verdict
	// triggered this (§17.1's own "no recursion" rule is checked again,
	// defensively, by the notifier itself before spawning -- see that
	// package's own doc comment).
	OriginReviewSessionID string `json:"originReviewSessionId"`
	// OriginHeadBranch is the literal value the fix PR's own Base will be
	// assigned to (never resolved via resolvePRBaseBranch, §17.2's
	// amendment) -- captured once, at claim time, from the origin
	// session's own repos config.
	OriginHeadBranch string `json:"originHeadBranch"`
	// RepoName/RepoCloneURL let the child session check out the SAME repo
	// the origin session did (SpawnChildSession's own req.Repos).
	RepoName     string `json:"repoName"`
	RepoCloneURL string `json:"repoCloneUrl"`
	// FindingIdentityHashes/FindingDescriptions are the specific
	// sentinel-kind findings this fix session is pre-loaded with (§17.2:
	// "pre-loaded with the origin diff and the specific sentinel
	// finding(s)") -- parallel slices, same order, same length.
	FindingIdentityHashes []string `json:"findingIdentityHashes"`
	FindingDescriptions   []string `json:"findingDescriptions"`
}
