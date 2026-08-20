// This file (reviewcostbudgetserver.go) gives internal/domain/reviewtriage.
// ShouldSkipOptionalPass (§26.7, costbudget.go) its first production call
// site (§26.5) -- that function's own doc comment states plainly it was
// shipped in §26.4 as a tested, exported pure function with ZERO
// production callers: "the actual mechanism putting §26.7's policy into
// effect is the review agent's OWN judgment" today, guided only by a
// dollar figure baked as prose into its own prompt (internal/domain/
// review/context.go's own subAgentOrchestrationInstructions, before this
// Step).
//
// reviewCostBudgetServer is sandbox-agent's own FIRST HTTP server -- no
// http.Server/http.HandleFunc existed anywhere in this binary before this
// Step (verified directly: sandbox-agent has only ever been an HTTP
// CLIENT, of `opencode serve` and of the control plane). It is a tiny,
// LOOPBACK-ONLY listener (bound to 127.0.0.1, never 0.0.0.0) serving
// exactly one route, GET /review-cost-budget, that a review turn's own
// agent calls via its own tool use (bash/curl -- this codebase's only
// "tool" pattern is a prompt-embedded URL the agent's own tool-use calls
// directly, reviewverdicttoolprompt.go's own precedent; there is no new
// WS message type, no AgentRuntime change, and no OpenCode plugin/tool
// registration here) to learn, as a real fact rather than a self-estimate,
// whether this review has already reached its own per-path cost ceiling.
//
// # Why an ephemeral port, not a fixed one
//
// A sandbox may also be running an arbitrary, user-authored .narvi/
// services.yml manifest (§14.2) whose own service ports are entirely
// user-defined -- a fixed, hardcoded port for this server could collide
// with one of those. Binding ":0" and reading the real port back
// (net.Listener.Addr) avoids that class of collision entirely, at the
// cost of needing the placeholder-substitution mechanism
// reviewcostbudgetprompt.go implements to tell a review turn's own prompt
// what the real URL turned out to be (this server does not exist yet,
// and so has no port at all, at the control-plane turn-creation time that
// prompt text is rendered).
//
// # Why no authentication
//
// This endpoint serves only numeric budget state (spent/ceiling/
// skip-or-not) -- never a secret -- and, being loopback-only, is
// reachable exclusively from inside this sandbox's own network namespace.
// review/context.go's own subAgentOrchestrationInstructions doc comment
// states the wider reasoning this inherits: the whole §26.7 mechanism is
// already agent-self-governed (the control plane has no channel to
// intervene inside an already-dispatched turn, §7's own anti-corruption-
// layer boundary), so there is no adversarial incentive for the agent
// calling this endpoint to lie about, or forge, what it receives -- the
// worst a dishonest or malfunctioning agent can do here is the SAME safe
// direction (skip an optional pass) a genuine failure already produces.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/khazaddev/narvi/internal/domain/reviewtriage"
)

// reviewCostBudgetServer wraps this sandbox's own loopback review-cost-
// budget listener and *http.Server -- a thin pairing so run() (main.go)
// can start it once, thread its own real URL into commandHandler, run its
// Accept loop through its own dedicated errgroup (deliberately a SEPARATE
// one from the WS bridge's -- see main.go's own budgetSrvGroup doc comment
// for why folding it into the SAME group as the bridge would deadlock on a
// fatal WS handshake status), and shut it down cleanly as part of the SAME
// bounded teardown sequence sup.StopAll already uses -- never a second,
// independently-tracked process class that could become a new orphan to
// force-kill (the exact class of leak the process supervisor already
// closed for a different subsystem; this server does not reintroduce it
// for a new one).
type reviewCostBudgetServer struct {
	listener net.Listener
	server   *http.Server
}

// startReviewCostBudgetServer binds a loopback-only ("127.0.0.1:0",
// EPHEMERAL port -- see this file's own top doc comment for why) TCP
// listener and constructs the *http.Server around it, but does not yet
// start serving -- the caller (run(), main.go) launches Serve on its own
// errgroup goroutine once the rest of boot has a chance to sequence
// around it, mirroring opencodeproc.Spawn's own "construct, then the
// caller decides when to actually run" shape.
//
// spentUSD is a method value (Adapter.CurrentTurnSpentUSD in every real
// caller) rather than a *opencode.Adapter field directly -- this package
// needs only "how much has this turn spent so far", never anything else
// the adapter exposes, and a narrow function type keeps this file
// trivially testable against a fake without constructing a real Adapter
// at all (reviewcostbudgetserver_test.go).
func startReviewCostBudgetServer(spentUSD func() (float64, bool), readHeaderTimeout time.Duration) (*reviewCostBudgetServer, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("sandbox-agent: listen for review-cost-budget server: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/review-cost-budget", reviewCostBudgetHandler(spentUSD))

	return &reviewCostBudgetServer{
		listener: listener,
		server: &http.Server{
			Handler:           mux,
			ReadHeaderTimeout: readHeaderTimeout,
		},
	}, nil
}

// URL returns this server's own real, already-bound
// "http://127.0.0.1:<port>/review-cost-budget" URL -- safe to call
// immediately after startReviewCostBudgetServer returns (net.Listen has
// already bound the port by the time it returns; Serve's own Accept loop
// need not have started yet).
func (s *reviewCostBudgetServer) URL() string {
	return "http://" + s.listener.Addr().String() + "/review-cost-budget"
}

// Serve runs this server's own Accept loop until Shutdown (or Close) is
// called from elsewhere -- mirrors cmd/control-plane/main.go's own
// IDENTICAL http.Server.ListenAndServe carve-out for the exact same
// reason: http.ErrServerClosed is Shutdown/Close's own documented,
// expected return value on a clean stop, never a real failure to
// propagate into run()'s own errgroup.
func (s *reviewCostBudgetServer) Serve() error {
	if err := s.server.Serve(s.listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("sandbox-agent: review-cost-budget server: %w", err)
	}
	return nil
}

// Shutdown gracefully stops this server -- a thin passthrough to
// http.Server.Shutdown (which itself closes the underlying listener),
// bounded by whatever deadline ctx carries (run()'s own caller supplies
// the SAME bounded shutdownCtx sup.StopAll uses, main.go).
func (s *reviewCostBudgetServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

// reviewCostBudgetResponse is this endpoint's own tiny JSON response
// shape -- deliberately NOT one of /contracts' formalized wire schemas
// (§6): this is a purely internal, loopback-only, ephemeral-per-sandbox
// protocol between an agent's own tool use and this SAME sandbox's own
// sandbox-agent process, never crossing the sandbox/control-plane
// boundary /contracts exists to pin -- exactly like the verdict-posting
// tool's own request shape (verdictToolInstructions, review/context.go)
// is hand-specified in prompt text rather than formalized there either.
type reviewCostBudgetResponse struct {
	SpentUSD   float64 `json:"spentUSD"`
	CeilingUSD float64 `json:"ceilingUSD"`
	ShouldSkip bool    `json:"shouldSkip"`
}

// reviewCostBudgetHandler answers GET /review-cost-budget?ceilingUsd=<usd>
// -- the "ceilingUsd" query parameter carries the per-path ceiling
// exactly as internal/domain/review's own subAgentOrchestrationInstructions
// rendered it into the review turn's prompt at turn-creation time
// (formatUSD(ctx.ReviewCostBudgetUSD), context.go) -- a literal, already-
// deterministic number the control plane computed, never scraped from
// free English prose (this codebase's own "typed field, never a marker
// parsed from markdown" discipline, §8.2's invariant, applied here to
// a query parameter rather than a JSON field only because a GET request
// has no natural body slot of its own).
//
// spentUSD is read from THIS sandbox's own live turnState accumulator
// (Adapter.CurrentTurnSpentUSD, internal/adapters/outbound/opencode/
// adapter.go) -- never supplied by the caller -- so the agent calling this
// endpoint cannot influence the one number that actually matters by
// lying in its own request; only ceilingUsd (already public, harmless
// information the agent's own prompt text already told it) travels on
// the wire from the agent's side.
//
// Any method other than GET, or a missing/malformed ceilingUsd, is a 4xx
// -- the review turn's own prompt instructs the agent to treat ANY
// non-2xx response identically to "shouldSkip": true (subAgentOrchestrationInstructions,
// context.go's own "fail-safe-toward-caution" posture), so returning an
// error status here is itself part of the safety design, never merely
// defensive plumbing.
func reviewCostBudgetHandler(spentUSD func() (float64, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "sandbox-agent: review-cost-budget: method not allowed, want GET", http.StatusMethodNotAllowed)
			return
		}

		ceilingUSD, err := strconv.ParseFloat(r.URL.Query().Get("ceilingUsd"), 64)
		// strconv.ParseFloat happily accepts "NaN"/"Inf"/"+Inf"/"-Inf"/
		// "Infinity" (any case) as successful parses with no error -- so a
		// NaN/±Inf ceilingUsd must be rejected explicitly here, identically
		// to a parse failure, or it would fall through to a 200 whose body
		// then fails to encode (encoding/json cannot represent NaN/±Inf,
		// json.NewEncoder.Encode below would error AFTER the 200 status
		// header is already written), leaving the caller a 2xx status with
		// an empty/truncated body -- exactly backwards for this endpoint's
		// own fail-safe contract, where a malformed input must produce a
		// non-2xx the agent's prompt already treats as "shouldSkip": true
		// (this handler's own doc comment above), never a 2xx it might
		// mistake for a real answer.
		if err != nil || math.IsNaN(ceilingUSD) || math.IsInf(ceilingUSD, 0) {
			http.Error(w, "sandbox-agent: review-cost-budget: missing or malformed ceilingUsd query parameter", http.StatusBadRequest)
			return
		}

		// !ok ("no live turn registered yet", a request racing the very
		// start of a turn -- Adapter.CurrentTurnSpentUSD's own doc comment)
		// is NOT surfaced as an error status: it is indistinguishable, from
		// this endpoint's own point of view, from "nothing spent yet", so
		// it is folded into spent=0 explicitly here -- never left as
		// whatever spentUSD's own zero-value-on-failure return happens to
		// be, and never trusted blindly, since a caller's own spentUSD
		// implementation is not contractually required to also zero its
		// first return value when reporting ok=false.
		spent, ok := spentUSD()
		if !ok {
			spent = 0
		}

		resp := reviewCostBudgetResponse{
			SpentUSD:   spent,
			CeilingUSD: ceilingUSD,
			ShouldSkip: reviewtriage.ShouldSkipOptionalPass(spent, ceilingUSD),
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Warn("sandbox-agent: encode review-cost-budget response failed", "error", err)
		}
	}
}
