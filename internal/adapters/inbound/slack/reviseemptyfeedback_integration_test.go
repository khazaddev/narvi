//go:build integration

// This file is the audit-remediation batch's own regression tests for the
// "revise: accepts empty feedback" finding: plandomain.MatchRevise documents
// ok=true, feedback=="" for a bare "revise:" (or whitespace-only feedback)
// as an EXPLICIT caller's-own-job case (verdict.go's own doc comment) --
// handleEvent (handler.go) now applies the SAME empty-feedback guard the
// pre-existing "Request changes" Block Kit modal already applies
// (interactive.go's own handleViewSubmission: "empty feedback text in
// view_submission, ignoring"), instead of silently dispatching a genuine
// plan_mode=true revision turn with nothing at all for the agent to act on.
// Mirrors planapprovalgate_integration_test.go's own newSlackPlanGateTestRig/
// seedAwaitingApprovalPlanForSlack conventions and turn_integration_test.go's
// own capture-slog's-own-default-logger precedent exactly (same package,
// same file set's own established helpers reused directly).
package slack_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	narvipg "github.com/narvidev/narvi/internal/adapters/outbound/postgres"
	"github.com/narvidev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestHandler_ReplyOnMappedThread_AwaitingPlan_RevisePrefix_EmptyFeedback_BlockedNoTurnCreated
// is table-driven (§11) over a bare "revise:" and a whitespace-only
// "revise:   " reply -- both match plandomain.MatchRevise with ok=true,
// feedback=="" (verdict_test.go's own TestMatchRevise already pins this
// exact contract), yet handleEvent must NOT dispatch a plan_mode=true turn
// with an empty prompt for either: no new turn is created, the plan's own
// status is untouched, and the DISTINCT, more specific
// ackEmptyReviseFeedbackText reply is posted -- LOW audit fix (SECOND
// fix-pass, "the honest reply reused for the new empty-feedback case is
// generic boilerplate") -- rather than the generic ackPlanAwaitingText
// TestHandler_ReplyOnMappedThread_AwaitingPlan_OrdinaryText_PostsHonestReply
// proves for a non-revise ordinary reply.
//
// COSMETIC/LOW audit fix (confirmed finding, "the new regression tests ...
// only cover a bare 'revise:' and 'revise:   ' (ASCII spaces) ... no test
// case exercises any non-ASCII-space whitespace variant"): the tab/
// newline/NBSP/ideographic-space/zero-width-space cases below close that
// gap at this full HTTP-level integration layer, mirroring
// verdict_test.go's own TestIsBlankFeedback unit-level coverage of the
// exact same variants.
func TestHandler_ReplyOnMappedThread_AwaitingPlan_RevisePrefix_EmptyFeedback_BlockedNoTurnCreated(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		text    string
	}{
		{name: "bare prefix, no feedback at all", channel: "C0PLANREVISEEMPTY1", text: "revise:"},
		{name: "prefix followed only by whitespace", channel: "C0PLANREVISEEMPTY2", text: "revise:   "},
		{name: "prefix followed only by a tab", channel: "C0PLANREVISEEMPTY3", text: "revise:\t\t"},
		{name: "prefix followed only by a newline", channel: "C0PLANREVISEEMPTY4", text: "revise:\n\n"},
		{name: "prefix followed only by NBSP (U+00A0)", channel: "C0PLANREVISEEMPTY5", text: "revise:\u00A0\u00A0"},
		{name: "prefix followed only by an ideographic space (U+3000)", channel: "C0PLANREVISEEMPTY6", text: "revise:\u3000"},
		{name: "prefix followed only by zero-width-space runes (U+200B)", channel: "C0PLANREVISEEMPTY7", text: "revise:\u200B\u200B"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTestPool(t)
			auditLog := narvipg.NewAuditLogStore(pool)

			recordingServer, recordedBodies := newFakeSlackRecordingWithUsersInfo(t, "unused", "unused@example.com")
			linkSlackIdentityForTest(ctx, t, pool, "U0TESTUSER", sqlcgen.UserRoleMaintainer)
			linkSlackIdentityForTest(ctx, t, pool, "U0OTHERUSER", sqlcgen.UserRoleMaintainer)

			rig := newSlackPlanGateTestRig(t, pool, recordingServer, auditLog)

			firstEnvelope := appMentionEnvelope("Ev0"+tc.channel+"001", tc.channel, "1700000060.000100", "", "start this task")
			req := signedSlackRequest(t, firstEnvelope)
			rec := httptest.NewRecorder()
			rig.handler(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("first mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
			}

			mapping, err := rig.threads.Get(ctx, tc.channel, "1700000060.000100")
			if err != nil {
				t.Fatalf("Get thread mapping: %v", err)
			}
			sessionID := mapping.SessionID

			firstTurns, err := rig.turns.ListForSession(ctx, sessionID)
			if err != nil || len(firstTurns) != 1 {
				t.Fatalf("ListForSession after first mention: turns=%v err=%v, want exactly 1", firstTurns, err)
			}

			// Move the producing turn to a terminal, plan_mode=true state and
			// seed an awaiting_approval plan atop it -- mirrors
			// planapprovalgate_integration_test.go's own identical setup.
			if _, err := rig.turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
				ID:          firstTurns[0].ID,
				Status:      sqlcgen.TurnStatusCompleted,
				CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
			}); err != nil {
				t.Fatalf("UpdateStatus: %v", err)
			}
			plan := seedAwaitingApprovalPlanForSlack(ctx, t, rig.plans, sessionID, firstTurns[0].ID)

			replyEnvelope := messageEnvelope("Ev0"+tc.channel+"002", tc.channel, "1700000060.000200", "1700000060.000100", tc.text)
			req2 := signedSlackRequest(t, replyEnvelope)
			rec2 := httptest.NewRecorder()
			rig.handler(rec2, req2)
			if rec2.Code != http.StatusOK {
				t.Fatalf("reply: status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
			}

			finalTurns, err := rig.turns.ListForSession(ctx, sessionID)
			if err != nil {
				t.Fatalf("ListForSession after reply: %v", err)
			}
			if len(finalTurns) != 1 {
				t.Fatalf("len(turns) after reply = %d, want exactly 1 (empty revise: feedback must never create a turn)", len(finalTurns))
			}

			var dbStatus sqlcgen.PlanStatus
			if err := pool.QueryRow(ctx, `SELECT status FROM plans WHERE id = $1`, plan.ID).Scan(&dbStatus); err != nil {
				t.Fatalf("query plan row: %v", err)
			}
			if dbStatus != sqlcgen.PlanStatusAwaitingApproval {
				t.Errorf("db status = %q, want %q (empty revise: feedback must never decide the plan)", dbStatus, sqlcgen.PlanStatusAwaitingApproval)
			}

			// The handler call above already ran synchronously (every ack
			// call it makes happens before rig.handler returns) -- requests
			// is a buffered channel, so every request it ever sent is
			// already sitting in it by now; drained non-blockingly, mirroring
			// planapprovalgate_integration_test.go's own identical
			// "already-populated buffered channel" drain pattern.
			var gotHonestReply bool
		drain:
			for {
				select {
				case got := <-recordedBodies:
					if got.path != "/chat.postMessage" {
						continue
					}
					if text, ok := got.body["text"].(string); ok && strings.Contains(text, "no feedback followed it") {
						gotHonestReply = true
					}
				default:
					break drain
				}
			}
			if !gotHonestReply {
				t.Error("no chat.postMessage call carried the empty-revise-feedback honest reply")
			}
		})
	}
}

// TestHandler_RevisePrefix_NonEmptyFeedback_LogsPlanModeTrueOnSuccess is
// this batch's own regression test for the SECOND, observability half of
// the finding ("neither Slack nor Linear logs the routing decision
// itself"): a non-empty revise: reply's own success log line
// ("slack: added turn") must now carry plan_mode=true, making the
// re-routing decision (an ordinary reply turned into a plan revision)
// observable -- mirrors turn_integration_test.go's own
// TestHandler_ReplyOnMappedThread_LogsSessionAndTurnID capture-slog's-
// own-default-logger precedent exactly.
func TestHandler_RevisePrefix_NonEmptyFeedback_LogsPlanModeTrueOnSuccess(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	auditLog := narvipg.NewAuditLogStore(pool)

	recordingServer, _ := newFakeSlackRecordingWithUsersInfo(t, "unused", "unused@example.com")
	linkSlackIdentityForTest(ctx, t, pool, "U0TESTUSER", sqlcgen.UserRoleMaintainer)
	linkSlackIdentityForTest(ctx, t, pool, "U0OTHERUSER", sqlcgen.UserRoleMaintainer)

	rig := newSlackPlanGateTestRig(t, pool, recordingServer, auditLog)

	channel := "C0PLANREVISELOG"
	firstEnvelope := appMentionEnvelope("Ev0PLANREVISELOG001", channel, "1700000065.000100", "", "start this task")
	req := signedSlackRequest(t, firstEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	mapping, err := rig.threads.Get(ctx, channel, "1700000065.000100")
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}
	sessionID := mapping.SessionID

	firstTurns, err := rig.turns.ListForSession(ctx, sessionID)
	if err != nil || len(firstTurns) != 1 {
		t.Fatalf("ListForSession after first mention: turns=%v err=%v, want exactly 1", firstTurns, err)
	}
	if _, err := rig.turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:          firstTurns[0].ID,
		Status:      sqlcgen.TurnStatusCompleted,
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	seedAwaitingApprovalPlanForSlack(ctx, t, rig.plans, sessionID, firstTurns[0].ID)

	// syncLogBuffer (handler_integration_test.go), not a bare bytes.Buffer:
	// this test's own reply below creates a plan-mode turn, which fires the
	// SAME fire-and-forget GetOrSpawn+EnsureDispatched dispatch trigger
	// every turn-creation call site uses -- the session's Actor can still
	// be mid-flight on its own background goroutine, logging through this
	// SAME redirected default logger, while this test's own goroutine
	// reads logOutput below. See syncLogBuffer's own doc comment for the
	// full race.
	logBuf := &syncLogBuffer{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	replyEnvelope := messageEnvelope("Ev0PLANREVISELOG002", channel, "1700000065.000200", "1700000065.000100", "revise: drop the retry logic")
	req2 := signedSlackRequest(t, replyEnvelope)
	rec2 := httptest.NewRecorder()
	rig.handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("reply: status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	logOutput := logBuf.String()
	if !strings.Contains(logOutput, "slack: added turn") {
		t.Fatalf("log output missing the success log line; got: %s", logOutput)
	}
	if !strings.Contains(logOutput, `"plan_mode":true`) {
		t.Errorf("success log line missing plan_mode=true -- the re-routing decision (ordinary reply -> plan revision) must be observable; got: %s", logOutput)
	}
}

// TestHandler_ReplyOnMappedThread_AwaitingPlan_RevisePrefix_EmptyFeedback_LogsAtInfoNotWarn
// is the LOW audit finding's own regression test ("log-level inconsistency
// between the new empty-feedback-guard branch and the pre-existing ...
// 'blocked by awaiting-approval plan' branch"): the empty-feedback branch's
// own log line must be emitted at Info, matching the functionally
// identical ErrPlanAwaitingApproval branch's own pre-existing Info-level
// log ("slack: reply blocked by awaiting-approval plan") -- both are
// routine, expected user mistakes producing the exact same honest reply
// and no adverse system state, so neither should out-rank the other on a
// Warn-level alert. Captures the real slog default logger's own JSON
// output exactly like TestHandler_RevisePrefix_NonEmptyFeedback_
// LogsPlanModeTrueOnSuccess just above, at LevelWarn this time
// specifically so a regression back to logger.Warn would still be
// captured (LevelInfo would also have worked, since Warn >= Info, but
// asserting the exact level string in the JSON output is what actually
// pins this down either way).
func TestHandler_ReplyOnMappedThread_AwaitingPlan_RevisePrefix_EmptyFeedback_LogsAtInfoNotWarn(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t)
	auditLog := narvipg.NewAuditLogStore(pool)

	recordingServer, _ := newFakeSlackRecordingWithUsersInfo(t, "unused", "unused@example.com")
	linkSlackIdentityForTest(ctx, t, pool, "U0TESTUSER", sqlcgen.UserRoleMaintainer)
	linkSlackIdentityForTest(ctx, t, pool, "U0OTHERUSER", sqlcgen.UserRoleMaintainer)

	rig := newSlackPlanGateTestRig(t, pool, recordingServer, auditLog)

	channel := "C0PLANREVISELOGLVL"
	firstEnvelope := appMentionEnvelope("Ev0PLANREVISELOGLVL001", channel, "1700000066.000100", "", "start this task")
	req := signedSlackRequest(t, firstEnvelope)
	rec := httptest.NewRecorder()
	rig.handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first mention: status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	mapping, err := rig.threads.Get(ctx, channel, "1700000066.000100")
	if err != nil {
		t.Fatalf("Get thread mapping: %v", err)
	}
	sessionID := mapping.SessionID

	firstTurns, err := rig.turns.ListForSession(ctx, sessionID)
	if err != nil || len(firstTurns) != 1 {
		t.Fatalf("ListForSession after first mention: turns=%v err=%v, want exactly 1", firstTurns, err)
	}
	if _, err := rig.turns.UpdateStatus(ctx, sqlcgen.UpdateTurnStatusParams{
		ID:          firstTurns[0].ID,
		Status:      sqlcgen.TurnStatusCompleted,
		CompletedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true},
	}); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	seedAwaitingApprovalPlanForSlack(ctx, t, rig.plans, sessionID, firstTurns[0].ID)

	// syncLogBuffer (handler_integration_test.go), not a bare bytes.Buffer
	// -- this specific reply hits the emptyReviseFeedback early-return
	// branch, which never reaches CreateTurnCore so no Actor is spawned for
	// THIS call, but this file's sibling test just above (identical capture
	// pattern, one webhook call away) does race exactly the way
	// syncLogBuffer's own doc comment describes; kept consistent here too
	// rather than leaving a second, currently-dormant instance of the same
	// unsafe-shared-writer pattern in this file for a future edit to wake
	// back up (mirrors linear's own identical precedent, commit 557a4fa).
	logBuf := &syncLogBuffer{}
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prevLogger) })

	replyEnvelope := messageEnvelope("Ev0PLANREVISELOGLVL002", channel, "1700000066.000200", "1700000066.000100", "revise:")
	req2 := signedSlackRequest(t, replyEnvelope)
	rec2 := httptest.NewRecorder()
	rig.handler(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("reply: status = %d, want 200 (body=%s)", rec2.Code, rec2.Body.String())
	}

	var gotLine string
	for _, line := range strings.Split(strings.TrimSpace(logBuf.String()), "\n") {
		if strings.Contains(line, "revise: reply had empty feedback") {
			gotLine = line
			break
		}
	}
	if gotLine == "" {
		t.Fatalf("log output missing the empty-feedback-guard log line; got: %s", logBuf.String())
	}
	if !strings.Contains(gotLine, `"level":"INFO"`) {
		t.Errorf(`empty-feedback-guard log line level != INFO (got line: %s) -- must match the functionally identical ErrPlanAwaitingApproval branch's own Info level, not a higher Warn severity for a routine user mistake`, gotLine)
	}
}
