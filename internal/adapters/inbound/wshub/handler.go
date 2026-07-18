package wshub

import "net/http"

// NewHandler builds the top-level dispatcher backing GET
// /sessions/{sessionID}/ws for BOTH ws types (§6.1 sandbox, §6.2 client):
// it peeks r.URL.Query().Get("type") and delegates to whichever of
// sandboxHandler/clientHandler matches ("sandbox" or "client"
// respectively); anything else, including a missing/empty type, is a 400.
//
// sandbox.go's own NewSandboxHandler keeps its own internal "type !=
// sandbox -> 400" check (harmless defense-in-depth, not removed by this
// Step) -- this dispatcher is what actually decides routing for the real
// GET /sessions/{sessionID}/ws route mounted in cmd/control-plane/main.go
// from this Step onward; NewSandboxHandler and NewClientHandler
// individually remain independently testable/constructible without going
// through this dispatcher at all (as this package's own existing and new
// tests both do).
func NewHandler(sandboxHandler, clientHandler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("type") {
		case "sandbox":
			sandboxHandler(w, r)
		case "client":
			clientHandler(w, r)
		default:
			http.Error(w, "unsupported or missing ws type", http.StatusBadRequest)
		}
	}
}
