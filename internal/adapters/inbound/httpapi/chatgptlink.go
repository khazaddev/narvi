// This file (chatgptlink.go) implements the browser-facing REST surface
// for the ChatGPT-account-OAuth link flow (§29.3/§29.9):
// POST/GET/DELETE /api/me/chatgpt-link. Self-service, own-user only --
// every one of these three handlers acts on the AUTHENTICATED caller's
// own userID (authenticatedUserID), never a path parameter naming a
// different user -- there is no "/api/me/{userID}/..." shape here, by
// design (§29.9: "self-service, own-user only"). Admin unlink-of-any-user
// mirrors §13.2's own admin-force-link precedent by reusing
// ActionManageMembers (the existing members-management action, admin-only)
// rather than a second action here -- but §29 specifies no concrete
// endpoint shape for that admin path, so this Step deliberately does not
// invent one; it is a named gap, not silently dropped (see this Step's
// own landing PR description).
//
// All three handlers are mounted behind auth.Middleware (cmd/control-
// plane/main.go), gated by authz.ActionLinkChatGPTAccount -- own-aware,
// the same row as ActionApprovePlan (§29.9) -- with Resource.OwnedOrJoined
// ALWAYS true: a "/me" endpoint is, by construction, always acting on the
// caller's own resource, so admin/maintainer/member (never viewer, per
// that action's own matrix row) all pass the ownership check
// unconditionally here; the action still correctly excludes viewers.

package httpapi

import (
	"net/http"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/app/chatgptlink"
	"github.com/khazaddev/narvi/internal/domain/authz"
	"github.com/khazaddev/narvi/internal/platform"
)

// chatgptLinkStatusResponse renders a chatgptlink.Status as the wire DTO
// -- shared by the POST and GET handlers below (§29.3: both report the
// current state; POST additionally kicks off a fresh attempt when none is
// live).
func chatgptLinkStatusResponse(status chatgptlink.Status) restdtos.ChatGPTLinkStatus {
	resp := restdtos.ChatGPTLinkStatus{Status: restdtos.ChatGPTLinkStatusStatus(status.Status)}
	if status.VerificationURL != "" {
		url := status.VerificationURL
		resp.VerificationUrl = restdtos.ChatGPTLinkStatusVerificationUrl(&url)
	}
	if status.UserCode != "" {
		code := status.UserCode
		resp.UserCode = restdtos.ChatGPTLinkStatusUserCode(&code)
	}
	if status.ExpiresAt != nil {
		resp.ExpiresAt = status.ExpiresAt
	}
	return resp
}

// StartChatGPTLink backs POST /api/me/chatgpt-link (§29.3 step 1):
// "Connect ChatGPT account" -- begins (or reuses a still-live) device-flow
// attempt for the authenticated caller.
func StartChatGPTLink(deps chatgptlink.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		if !authorize(w, r, authz.ActionLinkChatGPTAccount, authz.Resource{OwnedOrJoined: true}) {
			return
		}

		status, err := chatgptlink.StartLink(ctx, deps, userID)
		if err != nil {
			logger.Error("httpapi: chatgptlink.StartLink failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, chatgptLinkStatusResponse(status))
	}
}

// GetChatGPTLinkStatus backs GET /api/me/chatgpt-link (§29.3 step 2): the
// Settings page's own poll loop -- advances the current attempt by AT
// MOST one upstream call (chatgptlink.PollLink's own throttle), or simply
// reports the current linked/unlinked/needs_relink state when nothing is
// pending.
func GetChatGPTLinkStatus(deps chatgptlink.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		if !authorize(w, r, authz.ActionLinkChatGPTAccount, authz.Resource{OwnedOrJoined: true}) {
			return
		}

		status, err := chatgptlink.PollLink(ctx, deps, userID)
		if err != nil {
			logger.Error("httpapi: chatgptlink.PollLink failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, chatgptLinkStatusResponse(status))
	}
}

// DeleteChatGPTLink backs DELETE /api/me/chatgpt-link (§29.3: "unlink
// deletes it") -- idempotent, 204 whether or not an account was actually
// linked.
func DeleteChatGPTLink(deps chatgptlink.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		userID, ok := authenticatedUserID(w, r)
		if !ok {
			return
		}
		if !authorize(w, r, authz.ActionLinkChatGPTAccount, authz.Resource{OwnedOrJoined: true}) {
			return
		}

		if err := chatgptlink.Unlink(ctx, deps, userID); err != nil {
			logger.Error("httpapi: chatgptlink.Unlink failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
