// This file implements §30.2's layer 0: the transport gate.
//
// It is the only layer that sees everything. The typed decorator one level
// up records the six port writes with their real shapes and keeps the
// state machines coherent, but it can only see what goes through the port.
// Five mutating methods already exist on the concrete adapter outside it,
// the GitHub ingress posts synchronous comments through the same adapter
// instance without touching either the port or the outbox, and a twelfth
// mutating method added later would compile cleanly and be invisible to
// every typed layer. All of it rides the constructor-injected HTTP client,
// so a RoundTripper installed there is where that class of leak is
// contained.
//
// The rule is deny-by-default on writes, and it is not a host allowlist.
// In shadow, GET and HEAD pass through untouched -- reading a customer's
// repository leaves no trace and the platform cannot evaluate anything
// without it. Every other verb is intercepted, recorded, and answered with
// a synthesized success, whatever host it was aimed at. A host allowlist
// would be the wrong shape here: it fails open for anything nobody
// remembered to list, which is precisely the discipline this design exists
// to remove.

package githubapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/narvidev/narvi/internal/app/shadowledger"
	"github.com/narvidev/narvi/internal/domain/shadowsentinel"
)

// shadowRoundTripper intercepts mutating requests and records them.
//
// It holds no egress capability of its own: the decision of whether this
// request is live comes from the resolver, per call, keyed on the
// repository the request is aimed at. A gate that cached one answer would
// keep suppressing after a promotion, or keep emitting after a demotion.
type shadowRoundTripper struct {
	next    http.RoundTripper
	ledger  shadowledger.Store
	resolve func(ctx context.Context, repoFullName string) bool
}

// safeMethods pass through in shadow. Anything not in this set is
// intercepted -- the set is the allowlist, and it is a set of VERBS, not
// of hosts or paths, so a new endpoint cannot be added past it.
var safeMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
}

func (rt *shadowRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if safeMethods[req.Method] {
		return rt.next.RoundTrip(req)
	}

	repoFullName := repoFromPath(req.URL.Path)
	if repoFullName == "" {
		// Unattributable: a mutating request whose path names no
		// repository cannot be resolved against a per-repo flag, and it
		// must NOT be handed to the resolver to decide -- that would make
		// the gate's safety depend on how some other component answers
		// for the empty string. It cannot be recorded usefully either,
		// since the ledger is read per repository. So it fails, loudly,
		// with a message that says which request and why.
		return nil, fmt.Errorf("githubapi: refusing a %s to %q: no repository in the path, so this platform's egress mode for it cannot be resolved", req.Method, req.URL.Path)
	}
	if rt.resolve != nil && rt.resolve(req.Context(), repoFullName) {
		return rt.next.RoundTrip(req)
	}

	body, err := drainBody(req)
	if err != nil {
		return nil, fmt.Errorf("githubapi: read request body for the shadow ledger: %w", err)
	}

	// Record-or-fail (§30.6): if the ledger write fails, the caller gets an
	// error rather than a synthesized success. Nothing left this process,
	// so there is nothing to reconcile -- and a suppression nobody can
	// evidence is worse than a visible failure.
	if err := shadowledger.Record(req.Context(), rt.ledger, shadowledger.Entry{
		Operation:    "http_" + strings.ToLower(req.Method),
		RepoFullName: repoFullName,
		Target:       req.URL.Path,
		Spec: shadowledger.Transport{
			Method: req.Method,
			Path:   req.URL.Path,
			Host:   req.URL.Host,
			Body:   body,
		},
		SessionID: pgtype.UUID{},
	}); err != nil {
		return nil, err
	}

	return synthesizedResponse(req), nil
}

// repoFromPath pulls "owner/repo" out of a GitHub REST path.
//
// Every mutating GitHub endpoint this platform calls is under /repos/
// {owner}/{repo}/..., so the repository the write targets is recoverable
// from the URL alone -- which is what lets the gate resolve the flag for
// the RIGHT repository without the typed layer telling it.
//
// A path that does not match yields an empty string, and the caller
// refuses the request outright rather than asking the resolver about it.
// Delegating would make this gate's safety a property of how another
// component answers for "", which is not a property anyone stated.
func repoFromPath(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	if len(parts) < 3 || parts[0] != "repos" {
		return ""
	}
	return parts[1] + "/" + parts[2]
}

// drainBody reads the request body and puts a fresh reader back, so the
// request stays usable if a caller inspects it afterwards.
func drainBody(req *http.Request) (string, error) {
	if req.Body == nil {
		return "", nil
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return "", err
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	return string(raw), nil
}

// synthesizedResponse is what a suppressed request gets back.
//
// §30.6 requires synthetic results to be impossible to mistake for real
// ones. This one carries an explicit marker in its body and a header, so
// anything that logs or stores it shows what it is rather than looking
// like a GitHub response that happened to have odd ids.
func synthesizedResponse(req *http.Request) *http.Response {
	// The body carries the SAME sentinels the typed decorator returns, and
	// that is the point of this function rather than a stylistic detail.
	//
	// Two layers can suppress independently: the decorator resolves the
	// flag, and so does this gate underneath it. When they disagree --
	// which one transient repo_settings read failure is enough to cause,
	// since that read fails closed -- the decorator calls the live client
	// and gets THIS response back. A body with no fields parsed into
	// PRRef{Number: 0, URL: ""} and an empty commit SHA: zero values that
	// no synthetic-value check recognises, because those checks look for
	// the sentinels. Downstream lanes then ran against a pull request
	// that does not exist, and nothing failed to say so.
	//
	// Filling the fields a real response would carry makes a suppressed
	// result look the same whichever layer decided it, which is what lets
	// a caller check once.
	payload, _ := json.Marshal(map[string]any{
		"shadowSuppressed": true,
		"note":             "This platform is in shadow mode for this repository. Nothing was sent.",
		"number":           shadowsentinel.PRNumber,
		"html_url":         shadowsentinel.URLScheme + repoFromPath(req.URL.Path) + "/not-created",
		"sha":              shadowsentinel.CommitSHA,
		"commit":           map[string]any{"sha": shadowsentinel.CommitSHA},
	})
	h := make(http.Header)
	h.Set("Content-Type", "application/json")
	h.Set("X-Narvi-Shadow-Suppressed", "true")
	return &http.Response{
		Status:     "200 OK",
		StatusCode: http.StatusOK,
		Header:     h,
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Request:    req,
	}
}
