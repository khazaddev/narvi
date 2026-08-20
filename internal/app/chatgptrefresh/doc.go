// Package chatgptrefresh is the control plane's own single background
// refresh pump for ChatGPT-account OAuth credentials (§29.5) --
// a sibling of app/outboxworker/app/reconciler/app/imagebuild, not folded
// into any of them (TECHNICAL_PLAN.md §1's own repo-layout convention:
// one package per major loop/subsystem under internal/app/).
//
// Why a pump, never lazy-refresh-on-use: OpenAI rotates refresh tokens
// with reuse detection (§29.2), and its own guidance is explicit that one
// credential file belongs to a single holder, never shared across
// concurrent jobs. In Narvi, "concurrent jobs" would mean N sandboxes for
// one user racing each other into refresh_token_reused lockouts if each
// refreshed independently on first use — so the control plane is the
// SOLE refresher, and sandboxes never see a refresh token at all (§29.6).
//
// Pump.Run ticks every platform.Timeouts.ChatGPTOAuthRefreshPumpInterval,
// calling PumpOnce -- exported separately, exactly like outboxworker.
// Builder.PumpOnce/imagebuild.Builder.PumpOnce, so tests can drive exactly
// one tick deterministically.
//
// PumpOnce deliberately does NOT mirror outboxworker's own "claim a WHOLE
// BATCH in one short transaction, then do every slow network call OUTSIDE
// any transaction" shape. Here, EACH row's own claim + refresh + rewrite
// happen inside ONE transaction PER ROW, holding that one claimed row's
// own FOR UPDATE SKIP LOCKED lock for the duration of its own refresh
// call, committed immediately after -- see refreshClaimedRow's own doc
// comment (pump.go) for the full mechanism, including the S1 finding
// (adversarial review) that motivated committing per row instead of once
// per whole batch: a shared batch-wide transaction meant an interruption
// before its own single final commit could roll back every already-
// rotated row in the batch at once, even though each one's own refresh
// token had already been consumed upstream by then.
//
// Holding a lock across the live refresh call at all (rather than
// releasing it immediately, outboxworker-style) is still the deliberate
// deviation from outboxworker's own shape, not an oversight: outboxworker
// releases its claim lock immediately because ITS OWN failure mode (two
// builders concurrently delivering the SAME notification) is
// comparatively benign (a slightly-delayed duplicate), so it can afford a
// cheaper claim-then-release-then-act shape with a lease-based CAS
// renewal instead. Here, the failure mode a held lock prevents (two pump
// instances concurrently refreshing the SAME row) is exactly the
// "concurrent jobs sharing one credential" case OpenAI's own docs
// prohibit, with a real, costly consequence (a forced user re-link) --
// worth the cost of holding one row's own lock for the duration of one
// HTTP call, especially now that "one row's own lock" is the full extent
// of it, not a whole batch's.
package chatgptrefresh
