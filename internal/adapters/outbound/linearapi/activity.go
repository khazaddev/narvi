package linearapi

import "context"

// createAgentActivityMutation is Linear's own real agentActivityCreate
// mutation (verified against Linear's real GraphQL schema -- schema.
// graphql's own Mutation.agentActivityCreate(input: AgentActivityCreateInput!):
// AgentActivityPayload! -- and its own developer docs' worked example
// during Step 34's investigation). content is passed as a raw JSONObject,
// matching AgentActivityCreateInput.content's own schema type exactly
// ("This object is not strictly typed. See ... for typing details.").
const createAgentActivityMutation = `
mutation NarviCreateAgentActivity($input: AgentActivityCreateInput!) {
  agentActivityCreate(input: $input) {
    success
  }
}
`

type createAgentActivityData struct {
	AgentActivityCreate struct {
		Success bool `json:"success"`
	} `json:"agentActivityCreate"`
}

// thoughtContent is the "thought" activity content shape (Linear's real
// docs: `{"content": {"type": "thought", "body": "..."}}`) -- one of the
// five allowed activity content types; this Step's own scope only ever
// emits this one (a minimal, immediate acknowledgment, per this package's
// own doc.go), never action/response/error/elicitation.
type thoughtContent struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

// CreateThoughtActivity posts a single `thought` Agent Activity to
// agentSessionID, authenticated as the workspace's own installed app
// (accessToken, from a stored linear_installations row). This is the
// minimal, immediate acknowledgment Linear's own docs require within 10
// seconds of a `created` AgentSessionEvent to avoid the session being
// marked unresponsive -- see this package's own doc.go for why this is a
// direct, synchronous call rather than routed through an outbox.
func (c *Client) CreateThoughtActivity(ctx context.Context, accessToken, agentSessionID, body string) error {
	variables := map[string]any{
		"input": map[string]any{
			"agentSessionId": agentSessionID,
			"content": thoughtContent{
				Type: "thought",
				Body: body,
			},
		},
	}
	var data createAgentActivityData
	return c.doGraphQL(ctx, accessToken, createAgentActivityMutation, variables, &data)
}

// outcomeContent is the "response"/"error" activity content shape (Linear's
// real docs: `{"content": {"type": "response"|"error", "body": "..."}}"`) --
// two of the five allowed activity content types (thought/elicitation/
// action/response/error), verified against Linear's own current developer
// documentation during Step 35's ("outbox delivery") own investigation:
// emit "response" when "work has been completed or a final result is
// available", emit "error" to "report an error or failure". This Step's
// own scope only ever emits one of these two (a completed turn's
// success/failure outcome), never action/elicitation.
type outcomeContent struct {
	Type string `json:"type"`
	Body string `json:"body"`
}

// outcomeContentType is Type's own value in outcomeContent -- a small,
// named type (rather than a bare string) so CreateOutcomeActivity's own
// callers can never pass an arbitrary string where Linear's own fixed
// "response"/"error" vocabulary is expected.
type outcomeContentType string

const (
	outcomeContentTypeResponse outcomeContentType = "response"
	outcomeContentTypeError    outcomeContentType = "error"
)

// CreateResponseActivity posts a single `response` Agent Activity to
// agentSessionID -- Step 35's ("outbox delivery") own async notification
// for a turn that completed SUCCESSFULLY (turn.TriggerComplete), delivered
// via the outbox (internal/app/outboxworker), never synchronously from an
// inbound webhook handler the way CreateThoughtActivity above is.
func (c *Client) CreateResponseActivity(ctx context.Context, accessToken, agentSessionID, body string) error {
	return c.createOutcomeActivity(ctx, accessToken, agentSessionID, outcomeContentTypeResponse, body)
}

// CreateErrorActivity posts a single `error` Agent Activity to
// agentSessionID -- Step 35's own async notification for a turn that
// FAILED (turn.TriggerFail) or was cancelled (turn.TriggerCancel), the
// symmetric counterpart to CreateResponseActivity above.
func (c *Client) CreateErrorActivity(ctx context.Context, accessToken, agentSessionID, body string) error {
	return c.createOutcomeActivity(ctx, accessToken, agentSessionID, outcomeContentTypeError, body)
}

func (c *Client) createOutcomeActivity(ctx context.Context, accessToken, agentSessionID string, contentType outcomeContentType, body string) error {
	variables := map[string]any{
		"input": map[string]any{
			"agentSessionId": agentSessionID,
			"content": outcomeContent{
				Type: string(contentType),
				Body: body,
			},
		},
	}
	var data createAgentActivityData
	return c.doGraphQL(ctx, accessToken, createAgentActivityMutation, variables, &data)
}
