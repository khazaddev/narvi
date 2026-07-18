package contractstest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

var testTimestamp = time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)

func TestSandboxEventsRoundTrip(t *testing.T) {
	sch := compileSchema(t, "sandbox-ws/v1/events.schema.json", "")

	t.Run("Ready", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.Ready{
			Type:      "ready",
			MessageId: "e1",
			SessionId: testSessionID,
			Gen:       1,
			Timestamp: testTimestamp,
		})
	})

	t.Run("Heartbeat", func(t *testing.T) {
		conversationID := "conv-123"
		bootPhase := "installing_deps"
		roundTrip(t, sch, sandboxws.Heartbeat{
			Type:           "heartbeat",
			MessageId:      "e2",
			SessionId:      testSessionID,
			Gen:            1,
			ConversationId: &conversationID,
			LastBootPhase:  &bootPhase,
			Timestamp:      testTimestamp,
		})
	})

	t.Run("Heartbeat_BeforeFirstTurn", func(t *testing.T) {
		// Both conversationId and lastBootPhase are REQUIRED keys whose value
		// may be null (§6.1 nullability convention) -- this is not the
		// omission-means-fresh case (that's unique to Prompt.conversationId).
		roundTrip(t, sch, sandboxws.Heartbeat{
			Type:           "heartbeat",
			MessageId:      "e2b",
			SessionId:      testSessionID,
			Gen:            1,
			ConversationId: nil,
			LastBootPhase:  nil,
			Timestamp:      testTimestamp,
		})
	})

	t.Run("BootProgress", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.BootProgress{
			Type:      "boot_progress",
			MessageId: "e3",
			SessionId: testSessionID,
			Gen:       1,
			Phase:     "cloning_repos",
			Timestamp: testTimestamp,
		})
	})

	t.Run("Token", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.Token{
			Type:      "token",
			MessageId: "e4",
			SessionId: testSessionID,
			Gen:       1,
			Text:      "cumulative text so far",
		})
	})

	t.Run("ToolCall", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.ToolCall{
			Type:      "tool_call",
			MessageId: "e5",
			SessionId: testSessionID,
			Gen:       1,
			CallId:    "call-1",
			ToolName:  "read_file",
			Input:     sandboxws.ToolCallInput{"path": "main.go"},
		})
	})

	t.Run("ToolResult", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.ToolResult{
			Type:      "tool_result",
			MessageId: "e6",
			SessionId: testSessionID,
			Gen:       1,
			CallId:    "call-1",
			Output:    sandboxws.ToolResultOutput{"contents": "package main"},
			IsError:   false,
		})
	})

	t.Run("StepStart", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.StepStart{
			Type:      "step_start",
			MessageId: "e7",
			SessionId: testSessionID,
			Gen:       1,
			StepId:    "step-1",
		})
	})

	t.Run("StepFinish_FullCost", func(t *testing.T) {
		cached := 12
		usd := 0.0042
		roundTrip(t, sch, sandboxws.StepFinish{
			Type:      "step_finish",
			MessageId: "e8",
			SessionId: testSessionID,
			Gen:       1,
			StepId:    "step-1",
			Cost: sandboxws.StepFinishCost{
				Tokens: sandboxws.StepFinishCostTokens{
					Input:  1000,
					Output: 250,
					Cached: &cached,
				},
				Usd: &usd,
			},
		})
	})

	t.Run("StepFinish_MinimalCost", func(t *testing.T) {
		// cached and usd are optional; only tokens.input/tokens.output are
		// required (§6.1 nullability convention: this is the documented
		// exception, not the usual "required key, nullable value" shape).
		roundTrip(t, sch, sandboxws.StepFinish{
			Type:      "step_finish",
			MessageId: "e8b",
			SessionId: testSessionID,
			Gen:       1,
			StepId:    "step-1",
			Cost: sandboxws.StepFinishCost{
				Tokens: sandboxws.StepFinishCostTokens{
					Input:  10,
					Output: 5,
				},
			},
		})
	})

	t.Run("GitSync", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.GitSync{
			Type:      "git_sync",
			MessageId: "e9",
			SessionId: testSessionID,
			Gen:       1,
			Status:    sandboxws.GitSyncStatusCheckout,
			Branch:    "session/abc",
		})
	})

	t.Run("Artifact", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.Artifact{
			Type:         "artifact",
			MessageId:    "e10",
			SessionId:    testSessionID,
			Gen:          1,
			ArtifactType: sandboxws.ArtifactArtifactTypePr,
			Url:          "https://github.com/khazaddev/narvi/pull/42",
			Metadata:     sandboxws.ArtifactMetadata{"number": float64(42)},
		})
	})

	t.Run("ExecutionComplete", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.ExecutionComplete{
			Type:      "execution_complete",
			MessageId: "e11",
			SessionId: testSessionID,
			Gen:       1,
			AckId:     "execution_complete:e11",
			Outcome:   sandboxws.ExecutionCompleteOutcomeCompleted,
			Reason:    nil,
		})
	})

	t.Run("PushComplete", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.PushComplete{
			Type:      "push_complete",
			MessageId: "e12",
			SessionId: testSessionID,
			Gen:       1,
			AckId:     "push_complete:e12",
			Repos: []sandboxws.PushCompleteReposElem{
				{Name: "narvi", Branch: "session/abc", Sha: "deadbeef"},
			},
		})
	})

	t.Run("PushError", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.PushError{
			Type:      "push_error",
			MessageId: "e13",
			SessionId: testSessionID,
			Gen:       1,
			AckId:     "push_error:e13",
			Error:     "remote rejected non-fast-forward",
		})
	})

	t.Run("SessionTitle", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.SessionTitle{
			Type:      "session_title",
			MessageId: "e14",
			SessionId: testSessionID,
			Gen:       1,
			Title:     "Fix the failing test",
		})
	})

	t.Run("Warning", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.Warning{
			Type:      "warning",
			MessageId: "e15",
			SessionId: testSessionID,
			Gen:       1,
			Message:   "model catalog fallback in use",
		})
	})

	t.Run("Error", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.SandboxErrorEvent{
			Type:      "error",
			MessageId: "e16",
			SessionId: testSessionID,
			Gen:       1,
			AckId:     "error:e16",
			Message:   "opencode server crashed",
			Fatal:     true,
		})
	})

	t.Run("SnapshotReady", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.SnapshotReady{
			Type:       "snapshot_ready",
			MessageId:  "e17",
			SessionId:  testSessionID,
			Gen:        1,
			AckId:      "snapshot_ready:e17",
			SnapshotId: "snap-1",
		})
	})

	t.Run("SubTaskStart", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.SubTaskStart{
			Type:            "sub_task_start",
			MessageId:       "e18",
			SessionId:       testSessionID,
			Gen:             1,
			SubTaskId:       "prt_subtask1",
			Label:           "Investigate flaky test",
			ParentMessageId: "msg_parent1",
		})
	})

	t.Run("SubTaskFinish", func(t *testing.T) {
		roundTrip(t, sch, sandboxws.SubTaskFinish{
			Type:      "sub_task_finish",
			MessageId: "e19",
			SessionId: testSessionID,
			Gen:       1,
			AckId:     "sub_task_finish:e19",
			SubTaskId: "prt_subtask1",
			Outcome:   sandboxws.SubTaskFinishOutcomeCompleted,
		})
	})
}

// TestStepFinishCostTokensIsObjectNotNumber is the dedicated regression test
// called out explicitly in §6.1 and §9.1: "tokens is an object, not a
// number -- a number-vs-object mismatch here silently zeroes cost tracking,
// so pin it in the contract test." It asserts both directions: an
// object-shaped tokens payload is valid end to end, and a number-shaped one
// (what a naive/older OpenCode emitter might send) is rejected both by the
// JSON Schema and by the generated Go type.
func TestStepFinishCostTokensIsObjectNotNumber(t *testing.T) {
	sch := compileSchema(t, "sandbox-ws/v1/events.schema.json", "")

	validPayload := []byte(`{
		"type": "step_finish",
		"messageId": "e1",
		"sessionId": "` + testSessionID + `",
		"gen": 1,
		"stepId": "step-1",
		"cost": {"tokens": {"input": 100, "output": 50}}
	}`)

	t.Run("ObjectShapedTokensIsAccepted", func(t *testing.T) {
		if err := validateJSON(t, sch, validPayload); err != nil {
			t.Fatalf("expected object-shaped cost.tokens to validate, got: %v", err)
		}

		var event sandboxws.StepFinish
		if err := json.Unmarshal(validPayload, &event); err != nil {
			t.Fatalf("expected object-shaped cost.tokens to unmarshal, got: %v", err)
		}
		if event.Cost.Tokens.Input != 100 || event.Cost.Tokens.Output != 50 {
			t.Fatalf("unexpected decoded tokens: %#v", event.Cost.Tokens)
		}
	})

	// The regression this test exists to catch: cost.tokens sent as a bare
	// number (e.g. a total-token-count integer) instead of the
	// {input, output, cached?} object §6.1 mandates.
	numberShapedPayload := []byte(`{
		"type": "step_finish",
		"messageId": "e2",
		"sessionId": "` + testSessionID + `",
		"gen": 1,
		"stepId": "step-1",
		"cost": {"tokens": 150}
	}`)

	t.Run("NumberShapedTokensIsRejectedBySchema", func(t *testing.T) {
		if err := validateJSON(t, sch, numberShapedPayload); err == nil {
			t.Fatal("expected number-shaped cost.tokens to fail JSON Schema validation, got nil error")
		}
	})

	t.Run("NumberShapedTokensIsRejectedByGoUnmarshal", func(t *testing.T) {
		var event sandboxws.StepFinish
		if err := json.Unmarshal(numberShapedPayload, &event); err == nil {
			t.Fatal("expected number-shaped cost.tokens to fail Go unmarshal, got nil error")
		}
	})
}
