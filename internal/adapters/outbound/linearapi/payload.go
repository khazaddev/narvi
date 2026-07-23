package linearapi

// Payload is the JSON shape internal/app/outboxworker expects to find in
// an outbox entry's own payload column for a ports.NotificationKindLinear
// row -- enqueued by internal/app/sessionactor at turn-completion time
// (agent_session_id + organization_id from the session's own
// reverse-looked-up linear_agent_sessions row) with a short,
// human-readable outcome message.
//
// Deliberately carries organization_id, NEVER a decrypted access token: a
// notifier that needs Linear's real API credential looks it up fresh, by
// organization_id, at DELIVERY time via LinearInstallationStore +
// platform.DecryptToken (a hard security requirement -- see internal/app/
// outboxworker's own linearnotifier.go, which owns that lookup, for the
// full reasoning: a decrypted token must never sit in the outbox payload
// at rest).
type Payload struct {
	AgentSessionID string `json:"agent_session_id"`
	OrganizationID string `json:"organization_id"`
	// Text is the human-readable outcome message posted as the Agent
	// Activity's own body.
	Text string `json:"text"`
	// Success selects which outcome-shaped AgentActivity content type is
	// posted: true -> CreateResponseActivity ("response"), false ->
	// CreateErrorActivity ("error") -- mirroring
	// domain/turn.TriggerComplete vs TriggerFail/TriggerCancel.
	Success bool `json:"success"`
}
