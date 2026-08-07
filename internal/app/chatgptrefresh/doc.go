// Package chatgptrefresh is the control plane's own single background
// refresh pump for ChatGPT-account OAuth credentials (Step 59, §29.5) --
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
// PumpOnce deliberately does NOT mirror outboxworker's own "claim in one
// short transaction, then do the slow network call OUTSIDE any
// transaction" shape. Here, claim + refresh + rewrite all happen inside
// ONE transaction per batch, holding each claimed row's FOR UPDATE SKIP
// LOCKED lock for the duration of its own refresh call. This is a
// deliberate deviation, not an oversight: outboxworker's own shape exists
// to avoid holding a DB connection open across a POTENTIALLY large,
// slow, high-frequency batch of notifier calls (ticks every 5s); this
// pump ticks every 6h, expects a low single-digit row count per tick in
// realistic deployments, and — most importantly — the failure mode a
// held lock prevents (two pump instances concurrently refreshing the SAME
// row) is exactly the "concurrent jobs sharing one credential" case
// OpenAI's own docs prohibit, with a real, costly consequence (a forced
// user re-link) rather than outbox's own comparatively benign
// worst case (a slightly-delayed duplicate notification). See PumpOnce's
// own doc comment for the full shape.
package chatgptrefresh
