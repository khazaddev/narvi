package contractstest

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
)

// rest/v1/dtos.schema.json deliberately has no top-level oneOf (§6.3:
// "independent named payloads, not a discriminated union"), so each DTO is
// validated against its own $defs entry.
const restDTOsSchemaPath = "rest/v1/dtos.schema.json"

func TestSessionRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/Session")

	createdAt := time.Date(2026, 7, 15, 9, 0, 0, 0, time.UTC)
	updatedAt := time.Date(2026, 7, 16, 10, 30, 0, 0, time.UTC)

	t.Run("ActiveSession", func(t *testing.T) {
		title := "Fix the failing test"
		createdBy := testSessionID
		roundTrip(t, sch, restdtos.Session{
			Id:            testSessionID,
			Title:         &title,
			Status:        restdtos.SessionStatusActive,
			FailureReason: nil,
			Archived:      false,
			SpawnSource:   restdtos.SessionSpawnSourceWeb,
			CreatedBy:     &createdBy,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
	})

	t.Run("FailedSessionWithReason", func(t *testing.T) {
		// status/failureReason/spawnSource enums MUST match
		// migrations/000004_sessions.up.sql exactly (session_status,
		// session_failure_reason, session_spawn_source).
		roundTrip(t, sch, restdtos.Session{
			Id:            testSessionID,
			Title:         nil,
			Status:        restdtos.SessionStatusFailed,
			FailureReason: &restdtos.SessionFailureReason{Value: "timeout"},
			Archived:      true,
			SpawnSource:   restdtos.SessionSpawnSourceGithub,
			CreatedBy:     nil,
			CreatedAt:     createdAt,
			UpdatedAt:     updatedAt,
		})
	})
}

func TestCreateSessionRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateSessionRequest")

	title := "New session"
	prompt := "implement the feature"
	modelID := "claude-sonnet-5"
	branch := "main"
	// buildModelID is deliberately distinct from modelID above (audit finding
	// L16: BuildModelId had zero round-trip coverage anywhere in this file,
	// despite its own unusual type alias/struct tag, contracts/gen/go/
	// restdtos/restdtos.go's own CreateSessionRequestBuildModelId) -- a
	// reader diffing this fixture's own two model-id values against the
	// assertions below can tell modelID (the PLAN turn's own model) apart
	// from buildModelID (the eventual approval-dispatched IMPLEMENTATION
	// turn's own model, §12.2 item 3's own "plan: opus · build: sonnet").
	buildModelID := "claude-opus-4-8"

	roundTrip(t, sch, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceSlack,
		Title:       &title,
		Prompt:      &prompt,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: &branch},
		},
		ModelId:      &modelID,
		PlanMode:     true,
		BuildModelId: restdtos.CreateSessionRequestBuildModelId(&buildModelID),
	})
}

func TestCreateSessionRequestRoundTrip_NullOptionals(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateSessionRequest")

	// title/prompt/modelId null, and no first turn dispatched (§5.1 "single
	// CreateSessionRequest" shape used by every ingress surface).
	// BuildModelId is deliberately left at its Go zero value (nil) too --
	// unlike title/prompt/modelId above, this key is genuinely OPTIONAL
	// (`omitempty` in its own struct tag, see restdtos.
	// CreateSessionRequestBuildModelId's own schema doc comment), so a nil
	// value here exercises "the key is absent from the request body
	// entirely" rather than "present with a null value" -- the null-value
	// case is exercised by TestCreateSessionRequestRoundTrip's own explicit
	// buildModelID above.
	roundTrip(t, sch, restdtos.CreateSessionRequest{
		SpawnSource: restdtos.CreateSessionRequestSpawnSourceLinear,
		Title:       nil,
		Prompt:      nil,
		Repos: []restdtos.CreateSessionRequestReposElem{
			{Name: "narvi", Url: "https://github.com/khazaddev/narvi.git", Branch: nil},
		},
		ModelId:  nil,
		PlanMode: false,
	})
}

func TestCreateTurnRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateTurnRequest")

	modelID := "claude-sonnet-5"
	// AttachmentIds populated (Step 58, §28.5; review-fix coverage
	// addition, FIX H) -- this field had zero round-trip coverage before
	// this batch anywhere in this file.
	roundTrip(t, sch, restdtos.CreateTurnRequest{
		Prompt:        "continue where we left off",
		ModelId:       &modelID,
		PlanMode:      true,
		AttachmentIds: []string{testUploadID},
	})
}

func TestCreateTurnRequestRoundTrip_NullModelId(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateTurnRequest")

	roundTrip(t, sch, restdtos.CreateTurnRequest{
		Prompt:   "do the thing",
		ModelId:  nil,
		PlanMode: false,
	})
}

func TestCreateTurnResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/CreateTurnResponse")

	roundTrip(t, sch, restdtos.CreateTurnResponse{
		Id:     testSessionID,
		Status: restdtos.CreateTurnResponseStatusPending,
	})
}

func TestWSTokenResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/WSTokenResponse")

	roundTrip(t, sch, restdtos.WSTokenResponse{
		Token:     "ws-token-abc",
		ExpiresAt: time.Date(2026, 7, 17, 9, 0, 0, 0, time.UTC),
	})
}

// --- Members/audit-log DTOs (audit finding: wire-contract,
// internal/adapters/inbound/httpapi/members.go) -- these 8 shapes used to
// be hand-written Go structs entirely outside this schema-driven codegen
// pipeline (§13.2/§13.3's own members API); promoted here as a pure
// migration, so the round-trip coverage below mirrors the Session/
// CreateSessionRequest precedent above exactly rather than inventing a new
// style.

func TestIdentityRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/Identity")

	roundTrip(t, sch, restdtos.Identity{
		Id:         testSessionID,
		Provider:   restdtos.IdentityProviderSlack,
		ExternalId: "U12345",
		LinkedVia:  restdtos.IdentityLinkedViaAdmin,
		CreatedAt:  time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC),
	})
}

func TestMemberRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/Member")

	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	t.Run("WithIdentities", func(t *testing.T) {
		roundTrip(t, sch, restdtos.Member{
			Id:          testSessionID,
			Email:       "dev@example.com",
			DisplayName: "Dev Example",
			Role:        restdtos.MemberRoleMaintainer,
			Disabled:    false,
			CreatedAt:   createdAt,
			Identities: []restdtos.Identity{
				{
					Id:         testSandboxID,
					Provider:   restdtos.IdentityProviderGithub,
					ExternalId: "gh-12345",
					LinkedVia:  restdtos.IdentityLinkedViaAutoEmail,
					CreatedAt:  createdAt,
				},
			},
		})
	})

	t.Run("NoIdentities", func(t *testing.T) {
		// A member with zero currently-linked identities (e.g. a brand-new
		// account nobody has linked anything to yet) -- an empty, non-nil
		// slice here since this DTO's own "identities" key is schema-required
		// as a non-nullable array, and roundTrip's own reflect.DeepEqual
		// comparison needs marshal->unmarshal to reproduce whatever value
		// goes in.
		roundTrip(t, sch, restdtos.Member{
			Id:          testSandboxID,
			Email:       "disabled@example.com",
			DisplayName: "Disabled Example",
			Role:        restdtos.MemberRoleViewer,
			Disabled:    true,
			CreatedAt:   createdAt,
			Identities:  []restdtos.Identity{},
		})
	})
}

func TestPendingLinkPromptRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/PendingLinkPrompt")

	roundTrip(t, sch, restdtos.PendingLinkPrompt{
		Provider:   restdtos.PendingLinkPromptProviderLinear,
		ExternalId: "user-abc",
		ExpiresAt:  time.Date(2026, 7, 25, 9, 0, 0, 0, time.UTC),
		CreatedAt:  time.Date(2026, 7, 24, 9, 0, 0, 0, time.UTC),
	})
}

func TestListMembersResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/ListMembersResponse")

	createdAt := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	roundTrip(t, sch, restdtos.ListMembersResponse{
		Members: []restdtos.Member{
			{
				Id:          testSessionID,
				Email:       "dev@example.com",
				DisplayName: "Dev Example",
				Role:        restdtos.MemberRoleAdmin,
				Disabled:    false,
				CreatedAt:   createdAt,
				Identities:  []restdtos.Identity{},
			},
		},
		PendingLinkPrompts: []restdtos.PendingLinkPrompt{
			{
				Provider:   restdtos.PendingLinkPromptProviderSlack,
				ExternalId: "pending-slack-id",
				ExpiresAt:  createdAt.Add(time.Hour),
				CreatedAt:  createdAt,
			},
		},
	})
}

func TestAuditLogEntryRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/AuditLogEntry")

	createdAt := time.Date(2026, 7, 19, 8, 30, 0, 0, time.UTC)

	t.Run("WithActorAndCorrelation", func(t *testing.T) {
		actorUserID := testSessionID
		correlationID := "corr-abc-123"
		// Detail is json.RawMessage (goJSONSchema -> encoding/json.RawMessage
		// on this schema's own "detail" node, audit finding: LOW,
		// decode-then-re-encode precision loss) -- a compact literal with no
		// HTML-unsafe characters, so roundTrip's own marshal -> unmarshal ->
		// reflect.DeepEqual comparison isn't tripped up by encoding/json's
		// default HTML-escaping or by compact() reformatting whitespace that
		// wasn't there to begin with.
		roundTrip(t, sch, restdtos.AuditLogEntry{
			Id:            testSandboxID,
			ActorUserId:   &actorUserID,
			Action:        "member.role_changed",
			ResourceType:  "user",
			ResourceId:    testSessionID,
			Detail:        json.RawMessage(`{"from_role":"member","to_role":"maintainer"}`),
			CorrelationId: &correlationID,
			CreatedAt:     createdAt,
		})
	})

	t.Run("NoActorNoCorrelation", func(t *testing.T) {
		// Null actorUserId (a system/automation-attributed entry) and null
		// correlationId -- both nullable-but-required keys (§6.3's own
		// nullability convention: the key is always present, the value may
		// be null).
		roundTrip(t, sch, restdtos.AuditLogEntry{
			Id:            testSandboxID,
			ActorUserId:   nil,
			Action:        "identity.unlinked",
			ResourceType:  "identity",
			ResourceId:    testSessionID,
			Detail:        json.RawMessage(`{}`),
			CorrelationId: nil,
			CreatedAt:     createdAt,
		})
	})
}

func TestListAuditLogResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/ListAuditLogResponse")

	roundTrip(t, sch, restdtos.ListAuditLogResponse{
		Entries: []restdtos.AuditLogEntry{
			{
				Id:            testSandboxID,
				ActorUserId:   nil,
				Action:        "identity.force_linked",
				ResourceType:  "identity",
				ResourceId:    testSessionID,
				Detail:        json.RawMessage(`{"provider":"github"}`),
				CorrelationId: nil,
				CreatedAt:     time.Date(2026, 7, 19, 8, 30, 0, 0, time.UTC),
			},
		},
	})
}

func TestUpdateMemberRoleRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/UpdateMemberRoleRequest")

	roundTrip(t, sch, restdtos.UpdateMemberRoleRequest{
		Role: "maintainer",
	})
}

func TestLinkMemberIdentityRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/LinkMemberIdentityRequest")

	roundTrip(t, sch, restdtos.LinkMemberIdentityRequest{
		Provider:   "slack",
		ExternalId: "U12345",
	})
}

// --- Plan DTOs (audit-fix batch: M3, completeness -- GET .../plans had no
// way to ever discover a planId; L14/L16, wire-contract -- planapprove.go's
// own hand-written planActionResponse struct) -- Plan/ListPlansResponse back
// the new GET /api/sessions/:id/plans; PlanActionResponse promotes
// planapprove.go's former hand-written response struct now that this same
// area has a real DTO-consuming sibling endpoint. Follows TestMemberRoundTrip/
// TestListMembersResponseRoundTrip's own exact pattern above.

func TestPlanRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/Plan")

	createdAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)

	t.Run("AwaitingApproval", func(t *testing.T) {
		// Never decided yet: decidedAt/decidedBy both null, planModelId set
		// (copied from the producing turn's own model_id at creation time).
		planModelID := "claude-opus-4-8"
		roundTrip(t, sch, restdtos.Plan{
			Id:          testSessionID,
			SessionId:   testSandboxID,
			Version:     1,
			Status:      restdtos.PlanStatusAwaitingApproval,
			PlanModelId: restdtos.PlanPlanModelId(&planModelID),
			CreatedAt:   createdAt,
			DecidedAt:   nil,
			DecidedBy:   nil,
		})
	})

	t.Run("DecidedApproved", func(t *testing.T) {
		// Decided: decidedAt/decidedBy both set, planModelId null (the
		// producing turn had no explicit model id).
		decidedAt := createdAt.Add(time.Hour)
		decidedBy := testSessionID
		roundTrip(t, sch, restdtos.Plan{
			Id:          testSandboxID,
			SessionId:   testSessionID,
			Version:     2,
			Status:      restdtos.PlanStatusApproved,
			PlanModelId: nil,
			CreatedAt:   createdAt,
			DecidedAt:   &decidedAt,
			DecidedBy:   restdtos.PlanDecidedBy(&decidedBy),
		})
	})
}

func TestListPlansResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/ListPlansResponse")

	createdAt := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	roundTrip(t, sch, restdtos.ListPlansResponse{
		Plans: []restdtos.Plan{
			{
				Id:          testSessionID,
				SessionId:   testSandboxID,
				Version:     1,
				Status:      restdtos.PlanStatusSuperseded,
				PlanModelId: nil,
				CreatedAt:   createdAt,
				DecidedAt:   nil,
				DecidedBy:   nil,
			},
		},
	})
}

func TestPlanActionResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/PlanActionResponse")

	t.Run("Approved", func(t *testing.T) {
		turnID := testSandboxID
		roundTrip(t, sch, restdtos.PlanActionResponse{
			PlanId: testSessionID,
			Status: restdtos.PlanActionResponseStatusApproved,
			TurnId: restdtos.PlanActionResponseTurnId(&turnID),
		})
	})

	t.Run("Rejected", func(t *testing.T) {
		// RejectPlan never dispatches a new turn -- turnId is present but
		// null, matching this DTO's own "required key, nullable value"
		// convention (never omitted, per this schema's own top-level
		// nullability note).
		roundTrip(t, sch, restdtos.PlanActionResponse{
			PlanId: testSessionID,
			Status: restdtos.PlanActionResponseStatusRejected,
			TurnId: nil,
		})
	})
}

// TestMintUploadRequestRoundTrip covers Step 58's (§28.4/§28.5) upload-mint
// request DTO -- review-fix coverage addition (FIX H): this file had ZERO
// round-trip coverage for any of the three upload DTOs before this batch.
func TestMintUploadRequestRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/MintUploadRequest")

	roundTrip(t, sch, restdtos.MintUploadRequest{
		Filename:    "spec.pdf",
		ContentType: "application/pdf",
		SizeBytes:   4096,
	})
}

// TestMintUploadResponseRoundTrip covers the 201 response to a mint
// request: a presigned PUT URL, its own expiry, and the exact headers the
// uploader must send (§28.4).
func TestMintUploadResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/MintUploadResponse")

	expiresAt := time.Date(2026, 7, 15, 9, 15, 0, 0, time.UTC)
	roundTrip(t, sch, restdtos.MintUploadResponse{
		UploadId:  testUploadID,
		PutUrl:    "https://objstore.example.com/bucket/sessions/" + testSessionID + "/uploads/" + testUploadID,
		Headers:   restdtos.MintUploadResponseHeaders{"Content-Type": "application/pdf"},
		ExpiresAt: expiresAt,
	})
}

// TestConfirmUploadResponseRoundTrip covers both terminal outcomes confirm
// can report (§28.4/§28.6) -- crucially including the explicit-NULL
// failureReason case: the schema publishes failureReason as a REQUIRED key
// whose value type is ["string","null"], so an accidental `omitempty` added
// to ConfirmUploadResponse.FailureReason's own struct tag would silently
// drop the key entirely on the ready path and violate that contract without
// this test ever failing loudly if it only ever exercised the non-null
// case.
func TestConfirmUploadResponseRoundTrip(t *testing.T) {
	sch := compileSchema(t, restDTOsSchemaPath, "#/$defs/ConfirmUploadResponse")

	t.Run("ReadyWithExplicitNullFailureReason", func(t *testing.T) {
		roundTrip(t, sch, restdtos.ConfirmUploadResponse{
			Status:        restdtos.ConfirmUploadResponseStatusReady,
			FailureReason: nil,
		})
	})

	t.Run("FailedWithReason", func(t *testing.T) {
		roundTrip(t, sch, restdtos.ConfirmUploadResponse{
			Status:        restdtos.ConfirmUploadResponseStatusFailed,
			FailureReason: &restdtos.ConfirmUploadResponseFailureReason{Value: "verification_failed"},
		})
	})
}
