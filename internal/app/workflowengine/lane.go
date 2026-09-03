package workflowengine

import (
	"encoding/json"

	"github.com/narvidev/narvi/contracts/gen/go/restdtos"
	"github.com/narvidev/narvi/internal/domain/intent"
	"github.com/narvidev/narvi/internal/domain/reposource"
	"github.com/narvidev/narvi/internal/domain/workflow"
)

// resolveLane derives a session's Lane from its own raw
// sessions.intent_decision column (sqlcgen.Session.IntentDecision, JSONB,
// nullable, write-once per §18.4) -- pure, no I/O, table-driven-testable
// directly.
//
// rawIntentDecision is nil/empty for a session whose intent has not been
// classified yet -- not only "the classifier hasn't run", but genuinely
// racy in practice: internal/adapters/inbound/httpapi's own CreateSession
// handler calls recordExplicitIntentDecision AFTER CreateSessionCore (and
// therefore after that session's own first turn) already committed, so a
// session's first FEW turns can legitimately observe this column still
// NULL depending on which ingress surface and timing path created them.
// Rather than treat that as an error, this function folds a missing/
// malformed value into LaneFor's own existing fail-open branch by passing
// it empty target/mode strings -- LaneFor's own doc comment already
// promises those degrade to LaneRequest (unless mode alone happens to
// carry intent.ModePlan), which is exactly "what would have happened
// anyway" (§25.13): the built-in Request workflow is pure passthrough, so
// an unresolved lane never changes what actually dispatches.
func resolveLane(rawIntentDecision []byte) workflow.Lane {
	if len(rawIntentDecision) == 0 {
		return workflow.LaneFor("", "")
	}
	var rec intent.IntentDecisionRecord
	if err := json.Unmarshal(rawIntentDecision, &rec); err != nil {
		return workflow.LaneFor("", "")
	}
	return workflow.LaneFor(rec.Target, rec.Mode)
}

// repoFullNameFromSessionRepos derives a single unambiguous "owner/repo"
// full name from a session's own raw sessions.repos column (JSONB, the
// restdtos.CreateSessionRequestReposElem{name,url,branch} shape every
// session-creation path already persists it in) -- pure, no I/O.
//
// ok is true only when rawRepos decodes to EXACTLY one repo whose url
// parses as an owner/repo clone URL (reposource.ParseOwnerRepo, the same
// generic, host-agnostic parser pushpr.go/imagebuild already use). Zero
// repos, more than one, or a URL that fails to parse all return ("",
// false) -- a deliberate, documented judgment call (§25.4 says nothing
// about a multi-repo session's own binding-resolution repo), not an
// oversight: workflow_bindings' own repo-scoped rows are optional, entirely
// absent in this Step's own zero-config seed data, so falling back to the
// global binding (the caller's own next step on ok == false) can never
// select the wrong definition today -- it is the SAME built-in the global
// binding already resolves to regardless of which repo (if any) a session
// names. A future repo override only ever applies to the unambiguous
// single-repo case this function actually resolves.
func repoFullNameFromSessionRepos(rawRepos []byte) (string, bool) {
	if len(rawRepos) == 0 {
		return "", false
	}
	var repos []restdtos.CreateSessionRequestReposElem
	if err := json.Unmarshal(rawRepos, &repos); err != nil || len(repos) != 1 {
		return "", false
	}
	owner, repo, err := reposource.ParseOwnerRepo(repos[0].Url)
	if err != nil {
		return "", false
	}
	return owner + "/" + repo, true
}
