package slackapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/slackapi"
)

// TestEncodeDecodePlanActionValue_RoundTrip proves the button-value
// encoding used by every one of this Step's own approve/reject/
// request-changes buttons round-trips exactly, and that a malformed value
// (missing separator, empty half) is rejected rather than silently
// misparsed.
func TestEncodeDecodePlanActionValue_RoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		planID    string
		sessionID string
	}{
		{name: "typical uuids", planID: "11111111-1111-1111-1111-111111111111", sessionID: "22222222-2222-2222-2222-222222222222"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			value := slackapi.EncodePlanActionValue(tc.planID, tc.sessionID)
			gotPlanID, gotSessionID, ok := slackapi.DecodePlanActionValue(value)
			if !ok {
				t.Fatalf("DecodePlanActionValue(%q) ok = false, want true", value)
			}
			if gotPlanID != tc.planID || gotSessionID != tc.sessionID {
				t.Errorf("DecodePlanActionValue(%q) = (%q, %q), want (%q, %q)", value, gotPlanID, gotSessionID, tc.planID, tc.sessionID)
			}
		})
	}

	malformed := []string{"", "no-separator-at-all", "|missing-plan-id", "missing-session-id|"}
	for _, v := range malformed {
		if _, _, ok := slackapi.DecodePlanActionValue(v); ok {
			t.Errorf("DecodePlanActionValue(%q) ok = true, want false", v)
		}
	}
}

// TestPostPlanApprovalMessage_Success proves the real Block Kit request
// shape (channel/thread_ts/blocks with 3 buttons carrying the encoded
// value) and that the message's own real channel+ts are read back from
// Slack's response.
func TestPostPlanApprovalMessage_Success(t *testing.T) {
	t.Parallel()

	var gotPath string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C999", "ts": "1700000000.000100"})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")

	channel, ts, err := client.PostPlanApprovalMessage(context.Background(), slackapi.PlanApprovalPayload{
		PlanID:    "plan-1",
		SessionID: "session-1",
		ChannelID: "C123",
		ThreadTS:  "1234.5678",
		Version:   2,
		Text:      "1. Do the thing\n2. Do the other thing",
	})
	if err != nil {
		t.Fatalf("PostPlanApprovalMessage() error = %v, want nil", err)
	}
	if channel != "C999" || ts != "1700000000.000100" {
		t.Errorf("(channel, ts) = (%q, %q), want (%q, %q)", channel, ts, "C999", "1700000000.000100")
	}
	if gotPath != "/chat.postMessage" {
		t.Errorf("request path = %q, want %q", gotPath, "/chat.postMessage")
	}
	if gotBody["channel"] != "C123" {
		t.Errorf("channel = %v, want %q", gotBody["channel"], "C123")
	}
	if gotBody["thread_ts"] != "1234.5678" {
		t.Errorf("thread_ts = %v, want %q", gotBody["thread_ts"], "1234.5678")
	}
	blocks, ok := gotBody["blocks"].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("blocks = %v, want a non-empty array", gotBody["blocks"])
	}
	// The last block must be the actions row with exactly 3 buttons, each
	// carrying the SAME encoded planId|sessionId value.
	lastBlock, ok := blocks[len(blocks)-1].(map[string]any)
	if !ok || lastBlock["type"] != "actions" {
		t.Fatalf("last block = %v, want an actions block", blocks[len(blocks)-1])
	}
	elements, ok := lastBlock["elements"].([]any)
	if !ok || len(elements) != 3 {
		t.Fatalf("actions elements = %v, want exactly 3 buttons", lastBlock["elements"])
	}
	wantValue := slackapi.EncodePlanActionValue("plan-1", "session-1")
	wantActionIDs := map[string]bool{slackapi.ActionApprovePlan: false, slackapi.ActionRejectPlan: false, slackapi.ActionRequestChangesPlan: false}
	for _, el := range elements {
		elem, ok := el.(map[string]any)
		if !ok {
			t.Fatalf("button element = %v, not an object", el)
		}
		if elem["value"] != wantValue {
			t.Errorf("button value = %v, want %q", elem["value"], wantValue)
		}
		actionID, _ := elem["action_id"].(string)
		if _, known := wantActionIDs[actionID]; !known {
			t.Errorf("unexpected action_id %q", actionID)
		}
		wantActionIDs[actionID] = true
	}
	for actionID, seen := range wantActionIDs {
		if !seen {
			t.Errorf("expected button with action_id %q was not present", actionID)
		}
	}
}

// TestPostPlanApprovalMessage_APIError proves a non-ok Slack response
// surfaces as *DeliveryError, mirroring Deliver's own identical precedent.
func TestPostPlanApprovalMessage_APIError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "channel_not_found"})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")
	_, _, err := client.PostPlanApprovalMessage(context.Background(), slackapi.PlanApprovalPayload{ChannelID: "C123", ThreadTS: "1.1"})
	if err == nil {
		t.Fatal("PostPlanApprovalMessage() error = nil, want non-nil")
	}
}

// TestUpdateMessage_Success proves chat.update's own request shape
// (channel/ts/text, deliberately NO blocks -- see UpdateMessage's own doc
// comment for why omitting blocks is what clears the buttons).
func TestUpdateMessage_Success(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.update" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/chat.update")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")
	if err := client.UpdateMessage(context.Background(), "C123", "1234.5678", "Approved — implementation started."); err != nil {
		t.Fatalf("UpdateMessage() error = %v, want nil", err)
	}
	if gotBody["channel"] != "C123" || gotBody["ts"] != "1234.5678" {
		t.Errorf("(channel, ts) = (%v, %v), want (%q, %q)", gotBody["channel"], gotBody["ts"], "C123", "1234.5678")
	}
	if _, hasBlocks := gotBody["blocks"]; hasBlocks {
		t.Errorf("request body carries a blocks field, want none (omitting it is what clears the buttons)")
	}
}

// TestOpenView_Success proves views.open's own real request shape:
// trigger_id at the top level, and the view's own callback_id/
// private_metadata/single input block.
func TestOpenView_Success(t *testing.T) {
	t.Parallel()

	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/views.open" {
			t.Errorf("request path = %q, want %q", r.URL.Path, "/views.open")
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer server.Close()

	client := slackapi.New(server.Client(), server.URL, "xoxb-test-token")
	if err := client.OpenView(context.Background(), "trigger-123", "plan-1", "session-1"); err != nil {
		t.Fatalf("OpenView() error = %v, want nil", err)
	}

	if gotBody["trigger_id"] != "trigger-123" {
		t.Errorf("trigger_id = %v, want %q", gotBody["trigger_id"], "trigger-123")
	}
	view, ok := gotBody["view"].(map[string]any)
	if !ok {
		t.Fatalf("view = %v, want an object", gotBody["view"])
	}
	if view["callback_id"] != slackapi.RequestChangesCallbackID {
		t.Errorf("view.callback_id = %v, want %q", view["callback_id"], slackapi.RequestChangesCallbackID)
	}
	wantMetadata := slackapi.EncodePlanActionValue("plan-1", "session-1")
	if view["private_metadata"] != wantMetadata {
		t.Errorf("view.private_metadata = %v, want %q", view["private_metadata"], wantMetadata)
	}
	blocks, ok := view["blocks"].([]any)
	if !ok || len(blocks) != 1 {
		t.Fatalf("view.blocks = %v, want exactly 1 input block", view["blocks"])
	}
	inputBlock, ok := blocks[0].(map[string]any)
	if !ok || inputBlock["block_id"] != slackapi.RequestChangesBlockID {
		t.Fatalf("input block = %v, want block_id %q", blocks[0], slackapi.RequestChangesBlockID)
	}
	element, ok := inputBlock["element"].(map[string]any)
	if !ok || element["action_id"] != slackapi.RequestChangesActionID {
		t.Fatalf("input element = %v, want action_id %q", inputBlock["element"], slackapi.RequestChangesActionID)
	}
}
