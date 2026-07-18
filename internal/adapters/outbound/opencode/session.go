package opencode

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
)

// fallbackModel is this adapter's own pinned, last-resort default model
// reference (§7: "Model catalog: injected server-side config; must survive
// OpenCode upgrades -- on empty/failed catalog, fall back to a pinned
// known-good set"). NOT verified against any specific OpenCode
// installation's own live catalog — deliberately: the whole point of a
// pinned fallback is to survive an upgrade that silently drops or renames
// whatever this adapter would otherwise trust. Not specified by the plan;
// chosen as a well-known, broadly-available model slug, the same
// "not specified in the plan; chosen" spirit as platform/timeouts.go's own
// invented defaults.
const fallbackModel = "anthropic/claude-sonnet-4-5"

// resolveSession implements the Prompt command's own resume-vs-fresh
// contract (§3.3, commands.schema.json's own Prompt.conversationId doc
// comment): a set cmd.ConversationId is reused directly as the OpenCode
// sessionID (no new POST /session call); nil/absent creates one via
// POST /session.
func (a *Adapter) resolveSession(ctx context.Context, cmd sandboxws.Prompt) (string, error) {
	if cmd.ConversationId != nil && *cmd.ConversationId != "" {
		return *cmd.ConversationId, nil
	}

	var resp sessionResponse
	if err := a.doJSON(ctx, http.MethodPost, "/session", nil, &resp); err != nil {
		return "", fmt.Errorf("opencode: create session: %w", err)
	}
	if resp.ID == "" {
		return "", fmt.Errorf("opencode: create session: response carried no id")
	}
	return resp.ID, nil
}

// resolveModel implements §7's own minimal model-catalog-fallback quirk.
// raw is cmd.Model (sandboxws.PromptModel, a *string) as a plain *string —
// nil means "omit model entirely, let OpenCode use its own configured
// default" (never resolved against the catalog or fallback, since there is
// nothing to validate). A non-nil value that does not parse as
// "provider/model", or a best-effort GET /api/model catalog call that
// fails or returns empty, falls back to fallbackModel — deliberately
// minimal: this does NOT check whether the requested model is actually
// present in the catalog, only that a live catalog exists at all (§7's own
// framing: a version bump silently dropping the catalog entirely is what
// this guards against, not per-request model validation — a "fallback of
// last resort", not a rich model-selection feature).
func (a *Adapter) resolveModel(ctx context.Context, raw *string) *promptModelRef {
	if raw == nil {
		return nil
	}

	providerID, modelID, ok := strings.Cut(*raw, "/")
	if !ok || providerID == "" || modelID == "" {
		return fallbackModelRef()
	}

	var catalog modelCatalogResponse
	if err := a.doJSON(ctx, http.MethodGet, "/api/model", nil, &catalog); err != nil || len(catalog.Data) == 0 {
		return fallbackModelRef()
	}

	return &promptModelRef{ProviderID: providerID, ModelID: modelID}
}

func fallbackModelRef() *promptModelRef {
	providerID, modelID, _ := strings.Cut(fallbackModel, "/")
	return &promptModelRef{ProviderID: providerID, ModelID: modelID}
}

// postPromptAsync POSTs the translated turn to OpenCode's own
// prompt_async endpoint (§7: "POSTs prompt_async"). model is already
// resolved (resolveModel) before this is called.
//
// HONEST GAP: cmd.ScmName/cmd.ScmEmail (§6.1: Prompt "with author
// scmName/scmEmail for git attribution") are deliberately NOT threaded
// into this request or anywhere else in this package -- this Step's live
// research against the real /doc OpenAPI spec found no prompt_async (or
// any other OpenCode endpoint) accepting a git-author override, and
// commit-authorship wiring is Step 29's job ("gitstate in-sandbox"), not
// this Step's. cmd carries both fields end to end (they arrive on the
// wire Prompt command and reach this function unused), so no information
// is lost — a future Step can wire them once Step 29's own git-attribution
// mechanism exists to receive them.
func (a *Adapter) postPromptAsync(ctx context.Context, sessionID string, cmd sandboxws.Prompt, model *promptModelRef) error {
	body := promptAsyncRequest{
		Model: model,
		Parts: []promptPartInput{{Type: "text", Text: cmd.Text}},
	}
	path := "/session/" + url.PathEscape(sessionID) + "/prompt_async"
	return a.doJSON(ctx, http.MethodPost, path, body, nil)
}

// postAbort aborts sessionID's own in-flight turn, if any — this Step's
// own live research against the real OpenCode 1.17.15 binary found
// POST /session/{id}/abort as the real endpoint the wire "stop" command
// maps directly onto (§7 names "stop" as a command this adapter must
// handle, but does not itself name this specific OpenCode endpoint).
// OpenCode's own response is a bare true/false (VERIFIED live) — neither
// outcome is
// an error: false simply means there was nothing in flight to abort,
// exactly matching this adapter's own "Stop with nothing in flight is a
// safe no-op" contract (ports.AgentRuntime.Stop's own doc comment).
func (a *Adapter) postAbort(ctx context.Context, sessionID string) error {
	path := "/session/" + url.PathEscape(sessionID) + "/abort"
	var aborted bool
	return a.doJSON(ctx, http.MethodPost, path, nil, &aborted)
}

// fetchFinalMessages implements §7's own "final-state fetch fallback"
// quirk: GET /session/{id}/message, used only once the SSE stream has
// gone inactive for longer than platform.Timeouts.SSEInactivityTimeout
// without session.idle/session.error ever arriving.
func (a *Adapter) fetchFinalMessages(ctx context.Context, sessionID string) ([]messageListEntry, error) {
	path := "/session/" + url.PathEscape(sessionID) + "/message"
	var entries []messageListEntry
	if err := a.doJSON(ctx, http.MethodGet, path, nil, &entries); err != nil {
		return nil, fmt.Errorf("opencode: fetch final messages: %w", err)
	}
	return entries, nil
}
