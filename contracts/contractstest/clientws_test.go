package contractstest

import (
	"testing"

	"github.com/khazaddev/narvi/contracts/gen/go/clientws"
)

// client-ws/v1/protocol.schema.json deliberately has no top-level oneOf
// (§6.2: "these 4 shapes are independent named payloads, not a discriminated
// union"), so each payload is validated against its own $defs entry.
const clientWSSchemaPath = "client-ws/v1/protocol.schema.json"

func TestSubscribeRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, clientWSSchemaPath, "#/$defs/SubscribeRequest")

	roundTrip(t, sch, clientws.SubscribeRequest{
		Token:    "ws-token-abc",
		ClientId: "client-1",
	})
}

func TestSubscribedPayloadRoundTrip(t *testing.T) {
	sch := compileSchema(t, clientWSSchemaPath, "#/$defs/SubscribedPayload")

	roundTrip(t, sch, clientws.SubscribedPayload{
		SessionId: testSessionID,
		State:     clientws.SubscribedPayloadState{"status": "active"},
		Events: []clientws.SubscribedPayloadEventsElem{
			{"type": "token", "text": "hello"},
		},
		Artifacts: []clientws.SubscribedPayloadArtifactsElem{
			{"artifactType": "pr", "url": "https://github.com/khazaddev/narvi/pull/1"},
		},
		Participants: []clientws.SubscribedPayloadParticipantsElem{
			{"clientId": "client-1", "userId": "user-1"},
		},
	})
}

func TestFetchHistoryRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, clientWSSchemaPath, "#/$defs/FetchHistoryRequest")

	t.Run("WithCursorAndLimit", func(t *testing.T) {
		cursor := "cursor-abc"
		limit := 50
		roundTrip(t, sch, clientws.FetchHistoryRequest{
			SessionId: testSessionID,
			Cursor:    &cursor,
			Limit:     &limit,
		})
	})

	t.Run("NullCursorAndLimit", func(t *testing.T) {
		// null cursor means "start from the beginning/most recent"; null
		// limit means "use the server default page size" (§6.2).
		roundTrip(t, sch, clientws.FetchHistoryRequest{
			SessionId: testSessionID,
			Cursor:    nil,
			Limit:     nil,
		})
	})
}

func TestFetchHistoryResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, clientWSSchemaPath, "#/$defs/FetchHistoryResponse")

	t.Run("WithNextCursor", func(t *testing.T) {
		nextCursor := "cursor-def"
		roundTrip(t, sch, clientws.FetchHistoryResponse{
			Events: []clientws.FetchHistoryResponseEventsElem{
				{"type": "token", "text": "hello"},
			},
			NextCursor: &nextCursor,
		})
	})

	t.Run("NoMorePages", func(t *testing.T) {
		roundTrip(t, sch, clientws.FetchHistoryResponse{
			Events:     []clientws.FetchHistoryResponseEventsElem{},
			NextCursor: nil,
		})
	})
}
