package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/platform"
)

// maxWebhookBodyBytes bounds every GitHub webhook body this handler reads
// (via http.MaxBytesReader) -- mirrors internal/adapters/inbound/httpapi's
// own maxRequestBodyBytes precedent (1 MiB is generous for a comment
// body plus the surrounding PR/repo metadata GitHub includes).
const maxWebhookBodyBytes = 1 << 20 // 1 MiB

// githubDeliveryProvider is the "provider" value this adapter passes to
// postgres.WebhookDeliveryStore.Claim -- Step 31's own doc comment names
// this exact literal for GitHub.
const githubDeliveryProvider = "github"

// signatureHeaderPrefix is the fixed prefix GitHub's own
// "X-Hub-Signature-256" header value always carries before the actual hex
// digest.
const signatureHeaderPrefix = "sha256="

// Config bundles what NewHandler needs beyond the stores/registry already
// bundled in a *SessionCoalescer -- both required in every stage, see
// internal/platform/config.go's own gitHubWebhookSecretEnvVarName doc
// comment.
type Config struct {
	// WebhookSecret verifies "X-Hub-Signature-256" -- GitHub's own real
	// webhook secret, DISTINCT from platform.Config.HMACWebhookSecret.
	WebhookSecret string
	// BotHandle is the bot/app username mention detection matches comment
	// bodies against (compileMentionPattern, payload.go).
	BotHandle string

	// BotToken/PullRequests/Timeouts (batch fix/audit-github-pr-payload-
	// correctness, H5 audit fix) together resolve an issue_comment
	// mention's TRUE head branch/repo via one authenticated GitHub REST
	// API call -- see headresolve.go's own doc comments for the full
	// fallback behavior.
	//
	// BotToken is the SAME bot credential githubapi.BotNotifier already
	// authenticates its own PostIssueComment calls with
	// (platform.Config.GitHubBotToken) -- never a per-commenter
	// credential: a GitHub webhook mention carries no OAuth token for the
	// commenter (unlike CreatePR's own per-session, per-creator token),
	// and resolving a PR's own already-public head branch/repo needs none
	// of that per-user identity.
	//
	// PullRequests is nil-safe: nil (this package's own handler_test.go,
	// or any other minimal wiring that doesn't care about this fix) simply
	// keeps today's pre-fix fallback behavior for every issue_comment
	// mention -- see resolveIssueCommentHead's own doc comment.
	// githubapi.Adapter (the SAME instance production wiring already
	// constructs for CreatePR/ResolveBranchSHA/ResolveContractsFingerprint,
	// cmd/control-plane/main.go) satisfies this directly.
	//
	// Timeouts.GitHubGetPRTimeout bounds the one GetPullRequest call this
	// handler makes synchronously, inline, in its own request path --
	// mirrors internal/adapters/inbound/slack's own AckTimeout precedent
	// exactly (that field's own doc comment): a genuine outbound network
	// call made inline in a webhook handler must never run against the
	// bare, deadline-free r.Context() unbounded.
	BotToken     string
	PullRequests PullRequestResolver
	Timeouts     platform.Timeouts

	// Comments (Step 37/38 follow-up fix, Finding 1) posts the honest
	// planAwaitingApprovalReplyText reply (planawaitingreply.go) back to a
	// PR thread when coalesce.go's CreateOrJoin declines to enqueue a build
	// turn because the session's plan is currently awaiting approval. ALSO
	// posts actorNotAuthorizedReplyText (batch fix/deny-unlinked-github-
	// actors) for an UNLINKED commenter's denied mention -- the SAME
	// interface, never a second one, since both are just "post an honest
	// reply back to this PR thread". Nil-safe: nil (this package's own
	// handler_test.go, or any other minimal wiring that doesn't care about
	// either reply) simply skips posting -- see postPlanAwaitingReply's/
	// postActorNotAuthorizedReply's own doc comments. githubapi.Adapter
	// (the SAME instance production wiring already constructs for
	// PullRequests above) satisfies this directly.
	Comments CommentPoster

	// PublicBaseURL (batch fix/deny-unlinked-github-actors) is this
	// control plane's own externally-reachable base URL
	// (platform.Config.PublicBaseURL -- the SAME base identitylink.
	// BuildMagicLinkURL already uses, internal/app/identitylink/
	// service.go), used to build the sign-in URL
	// (PublicBaseURL+signInPath) actorNotAuthorizedReplyText points an
	// unlinked commenter at. Empty (this package's own handler_test.go, or
	// any other minimal wiring) simply renders a base-less/relative-looking
	// URL in that reply -- harmless for tests that never post a real
	// comment, but production wiring (cmd/control-plane/main.go) always
	// sets this from cfg.PublicBaseURL.
	PublicBaseURL string

	// LinkNotices (batch fix/deny-unlinked-github-actors) backs
	// claimActorNotAuthorizedNotice's own anti-spam dedupe check
	// (actornotauthorizedreply.go): one row per (repo, PR, commenter)
	// already told to sign in, atomically claimed against
	// Timeouts.GitHubActorNoticeTTL. Nil-safe: nil (this package's own
	// handler_test.go, or any other minimal wiring that doesn't care about
	// this dedupe) always allows the reply -- see that function's own doc
	// comment for why. Typed as the narrow ActorLinkNoticeClaimer
	// interface (not the concrete store type) so a test can inject a fake
	// that forces a genuine claim failure -- postgres.
	// NewGitHubActorLinkNoticeStore(pool) (the SAME pool every other store
	// in this Config/SessionCoalescer already shares) satisfies this
	// directly in production.
	LinkNotices ActorLinkNoticeClaimer
}

// NewHandler builds the POST /webhooks/github handler (cmd/control-plane/
// main.go). See doc.go's own "Request handling" section for the full
// verify -> dedupe-claim -> parse -> detect -> coalesce sequencing this
// implements.
func NewHandler(coalescer *SessionCoalescer, deliveries *postgres.WebhookDeliveryStore, cfg Config) http.HandlerFunc {
	mentionRE := compileMentionPattern(cfg.BotHandle)
	secret := []byte(cfg.WebhookSecret)

	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		logger := platform.Logger(ctx)

		r.Body = http.MaxBytesReader(w, r.Body, maxWebhookBodyBytes)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(err, &maxBytesErr) {
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Signature verification happens over these EXACT raw bytes,
		// fail-closed on ANY error (missing header, malformed hex,
		// mismatch) -- see platform.VerifyWebhookSignature's own doc
		// comment (internal/platform/webhooksig.go).
		presentedSig := strings.TrimPrefix(r.Header.Get("X-Hub-Signature-256"), signatureHeaderPrefix)
		if err := platform.VerifyWebhookSignature(secret, body, presentedSig); err != nil {
			logger.Warn("github: webhook signature verification failed", "error", err)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		deliveryID := r.Header.Get("X-GitHub-Delivery")
		if deliveryID == "" {
			logger.Warn("github: webhook request missing X-GitHub-Delivery header")
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		claim, err := deliveries.Claim(ctx, githubDeliveryProvider, deliveryID)
		if err != nil {
			logger.Error("github: claim webhook delivery failed", "error", err, "delivery_id", deliveryID)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if !claim.Inserted {
			// A redelivery of an already-claimed delivery id -- L4 audit
			// fix: GitHub does NOT automatically retry/redeliver a webhook
			// on a non-2xx response or a timeout (corrected from this
			// comment's own previous, factually wrong claim that it does);
			// redelivery is manual-only, triggered by a human via GitHub's
			// own "Redeliver" UI/API action once they notice a failure and
			// choose to retry it. Acknowledge without reprocessing either
			// way.
			logger.Info("github: duplicate webhook delivery, skipping", "delivery_id", deliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}

		eventType := r.Header.Get("X-GitHub-Event")
		m, ok, err := parseMention(eventType, body, mentionRE)
		if err != nil {
			logger.Error("github: parse webhook payload failed", "error", err, "event_type", eventType, "delivery_id", deliveryID)
			// This delivery was claimed above but never actually processed --
			// release the claim so that IF a human notices this failure and
			// manually redelivers this same X-GitHub-Delivery id (L4 audit
			// fix: via GitHub's own "Redeliver" UI/API action -- GitHub does
			// NOT automatically retry/redeliver on a non-2xx response or a
			// timeout, corrected from this comment's own previous, factually
			// wrong claim that it does), that redelivery can actually
			// reprocess it rather than being silently skipped forever as an
			// already-claimed duplicate.
			if releaseErr := deliveries.Release(ctx, githubDeliveryProvider, deliveryID); releaseErr != nil {
				logger.Error("github: release webhook delivery claim failed", "error", releaseErr, "delivery_id", deliveryID)
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !ok {
			// Not an event type this adapter acts on, not a PR comment, a
			// non-"created" comment action, or the bot wasn't mentioned --
			// acknowledge, nothing to do.
			w.WriteHeader(http.StatusOK)
			return
		}

		// M14 audit fix (completeness, self-comment filter): a comment
		// authored by the bot's OWN GitHub identity must never be treated as
		// a fresh mention-worthy event. Without this, the bot's own posted
		// comment (githubapi.Adapter's async turn-outcome notification back
		// to this SAME PR, wired via GitHubBotToken) could itself satisfy
		// compileMentionPattern -- e.g. quoting or echoing the handle back --
		// and re-trigger mention detection, a bot-replies-to-its-own-comment
		// loop this filter closes. Checked as early as possible (before the
		// resolveIssueCommentHead network call and before actor
		// resolution/CreateOrJoin below), so a self-authored comment does
		// none of that work either.
		//
		// cfg.BotHandle (m.CommenterLogin's own comparison target) is the
		// best available signal for "is this the bot" today -- the SAME
		// configured handle mention-detection itself already matches comment
		// bodies against (Config.BotHandle's own doc comment). internal/
		// platform/config.go's own GitHubBotToken doc comment describes that
		// credential as "a real GitHub personal access token or a GitHub App
		// installation token, whichever the deploying operator provisions"
		// -- so this filter must recognize BOTH realistic shapes a
		// comment.user.login can take for that SAME configured bot: a plain
		// PAT-authenticated bot account, whose own login matches BotHandle
		// exactly, and a GitHub App installation, which always posts its own
		// comments under the fixed, well-known "<slug>[bot]" login form
		// (e.g. "narvi-bot[bot]" for a configured handle of "narvi-bot" --
		// this is a standard GitHub convention, not a vague edge case).
		// Matching both closes the gap this comment used to document as an
		// unresolved, known limitation: a GitHub App's own turn-outcome
		// comment on its own PR is now correctly filtered, not
		// mistaken for a fresh mention. Two genuine residual gaps remain,
		// both accepted as out of this minimal filter's scope:
		//  - Under-inclusion: if the deploying operator ever reconfigures
		//    BotHandle to a value that no longer matches the ACTUAL identity
		//    posting comments (the PAT account renamed, or the GitHub App
		//    installed under a different slug than the newly configured
		//    handle), this filter -- keyed entirely off BotHandle -- would
		//    silently stop recognizing that identity's own comments as
		//    self-comments. Closing that fully would need this codebase to
		//    independently discover/verify the bot's own real login (e.g. a
		//    GET /user call against GitHubBotToken at startup).
		//  - Over-inclusion (the "[bot]" branch specifically): GitHub App
		//    slugs are globally unique, but nothing stops an unrelated,
		//    independently-installed third-party App from happening to share
		//    this deployment's configured BotHandle string -- that app's own
		//    genuine comment on the same PR would then also match and be
		//    silently dropped, never spawning a session/turn. Low likelihood
		//    (requires that exact slug coincidence) and not a new class of
		//    risk (the plain-BotHandle-equality branch already accepts the
		//    analogous risk for a human/org account named exactly
		//    BotHandle), but worth naming alongside the drift gap above.
		// Compared case-insensitively, mirroring compileMentionPattern's own
		// case-insensitive ("(?i)") mention matching.
		if m.CommenterLogin != "" && cfg.BotHandle != "" &&
			(strings.EqualFold(m.CommenterLogin, cfg.BotHandle) || strings.EqualFold(m.CommenterLogin, cfg.BotHandle+"[bot]")) {
			w.WriteHeader(http.StatusOK)
			return
		}

		// H5 audit fix (batch fix/audit-github-pr-payload-correctness):
		// issue_comment's own payload never carries the PR's real head
		// branch/repo directly (see issueCommentPayload's own doc comment)
		// -- resolve it here, via one authenticated GitHub API call, BEFORE
		// m is turned into the session's own repo spec below.
		// pull_request_review_comment already carries the real head
		// branch/repo straight out of parseMention, so this is never
		// called for that event type. Bounded by its own timeout (never
		// the bare, deadline-free ctx) -- see Config.Timeouts' own doc
		// comment for why; resolveIssueCommentHead itself never fails this
		// request, only logs and falls back.
		if eventType == eventTypeIssueComment {
			resolveCtx, cancel := context.WithTimeout(ctx, cfg.Timeouts.GitHubGetPRTimeout)
			m = resolveIssueCommentHead(resolveCtx, logger, cfg.PullRequests, cfg.BotToken, m)
			cancel()
		}

		req := restdtos.CreateSessionRequest{
			SpawnSource: restdtos.CreateSessionRequestSpawnSourceGithub,
			Prompt:      restdtos.CreateSessionRequestPrompt(&m.CommentBody),
			Repos: []restdtos.CreateSessionRequestReposElem{
				{
					Name:   m.RepoName,
					Url:    m.RepoCloneURL,
					Branch: restdtos.CreateSessionRequestReposElemBranch(m.HeadBranch),
				},
			},
		}

		// Batch fix/audit-github-actor-rbac's own addition (the H4 audit
		// finding this closes, see identity.go's own top doc comment):
		// resolve the real commenter behind m.CommenterID to a Narvi
		// user_id, ONCE, regardless of which CreateOrJoin branch this
		// mention ends up taking -- a direct identities lookup, never an
		// auto-linking algorithm (unlike Slack/Linear). actor stays invalid
		// for a commenter who has genuinely never signed into Narvi --
		// batch fix/deny-unlinked-github-actors means that invalid actor
		// is now DENIED by coalesce.go's own AuthorizeLinkedActor gates,
		// not bot-attributed allow-and-proceed (see this branch's own
		// ErrActorNotAuthorized handling below).
		//
		// err here is DISTINCT from "genuinely never linked" (a confirmed
		// audit finding on an earlier version of this batch: the two used
		// to be conflated into the same invalid pgtype.UUID{}, so a
		// transient identities-lookup error on an ALREADY-linked, fully-
		// authorized commenter -- e.g. a DB connection reset -- silently
		// became a permanent, wrongly-worded "I don't recognize your
		// GitHub account" denial for that real user). A non-nil err here
		// means resolveCommenterActor's own lookup itself failed for a
		// reason that says NOTHING about whether this commenter is
		// linked -- treated exactly like every other genuine backend
		// failure in this handler (parseMention's own error path above,
		// the generic CreateOrJoin failure path below): release the claim
		// so a real GitHub redelivery can retry once the backend recovers,
		// and never post the (potentially false) "please sign in" reply.
		actor, err := resolveCommenterActor(ctx, coalescer.Identities, m.CommenterID)
		if err != nil {
			logger.Error("github: resolve commenter identity failed", "error", err, "repo", m.RepoFullName, "pr_number", m.PRNumber)
			if releaseErr := deliveries.Release(ctx, githubDeliveryProvider, deliveryID); releaseErr != nil {
				logger.Error("github: release webhook delivery claim failed", "error", releaseErr, "delivery_id", deliveryID)
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		session, turn, isNew, err := coalescer.CreateOrJoin(ctx, m.RepoFullName, m.PRNumber, req, actor)
		if err != nil {
			if errors.Is(err, ErrActorNotAuthorized) {
				// ErrActorNotAuthorized fires for TWO distinct reasons
				// (coalesce.go already logged which, specifically) --
				// batch fix/deny-unlinked-github-actors' own addition
				// distinguishes them here using the SAME already-resolved
				// actor value passed into CreateOrJoin above, never a
				// second lookup:
				//
				//  - !actor.Valid: an UNLINKED commenter -- this batch's
				//    own repo-owner-decided hardening (aligning GitHub
				//    with Slack/Linear, see coalesce.go's own updated doc
				//    comment) now denies this instead of the pre-batch
				//    bot-attributed allow. Unlike a linked-but-denied
				//    actor below, this commenter has had NOTHING happen
				//    for them at all and no other channel to learn why --
				//    GitHub has no magic-link/pending-link mechanism to
				//    fall back on (identity.go's own top doc comment) --
				//    so this is the one case that gets an actionable
				//    reply, gated by claimActorNotAuthorizedNotice's own
				//    atomic anti-spam dedupe so a repeat mention within
				//    GitHubActorNoticeTTL doesn't spam the thread again
				//    (and so concurrent deliveries of the SAME still-
				//    unlinked commenter's mention can't both slip past the
				//    dedupe check and both post -- see that function's own
				//    doc comment, actornotauthorizedreply.go).
				//  - actor.Valid: a LINKED commenter whose role failed
				//    domain/authz.Authorize (e.g. a viewer denied
				//    ActionCreateSession, or a non-owning member denied
				//    ActionPromptSession) -- pre-existing, out of this
				//    batch's own scope, so this keeps today's silent-200
				//    behavior completely unchanged: that commenter already
				//    knows they're signed in, and "sign in via GitHub
				//    OAuth" would be confusing and wrong to tell them.
				//
				// Either way, acknowledge without releasing the claim:
				// unlike a transient failure, retrying a genuine
				// redelivery of the SAME comment would render the exact
				// same denial again, so there is nothing a GitHub retry
				// could fix here.
				if !actor.Valid {
					if claimActorNotAuthorizedNotice(ctx, logger, cfg.LinkNotices, cfg.Timeouts.GitHubActorNoticeTTL, m.RepoFullName, m.PRNumber, m.CommenterID) {
						postActorNotAuthorizedReply(ctx, logger, cfg.Comments, cfg.PublicBaseURL, cfg.BotToken, m.RepoFullName, m.PRNumber, m.CommenterID)
					}
				}
				w.WriteHeader(http.StatusOK)
				return
			}
			if errors.Is(err, httpapi.ErrPlanAwaitingApproval) {
				// Step 37/38 follow-up fix (Finding 1): the session's plan
				// is currently awaiting approval, so the REUSE path's own
				// httpapi.CreateTurnForBot call (coalesce.go) declined to
				// enqueue an ordinary build turn -- a deterministic,
				// expected business state, not a transient failure. Mirrors
				// ErrActorNotAuthorized's own "no release, retrying changes
				// nothing" reasoning exactly: the awaiting-plan condition
				// persists until a human decides (elsewhere -- see
				// planAwaitingApprovalReplyText's own doc comment), so
				// releasing the claim for a GitHub redelivery retry would
				// only ever reproduce this SAME outcome, forever, as a
				// self-inflicted retry storm. Unlike ErrActorNotAuthorized,
				// this ALSO posts an honest reply on the PR thread --
				// today's PRE-fix behavior left the thread with no reply at
				// all, unlike Slack/Linear's own equivalent honest replies
				// for this exact sentinel.
				logger.Info("github: mention blocked by awaiting-approval plan", "repo", m.RepoFullName, "pr_number", m.PRNumber)
				postPlanAwaitingReply(ctx, logger, cfg.Comments, cfg.BotToken, m.RepoFullName, m.PRNumber)
				w.WriteHeader(http.StatusOK)
				return
			}
			logger.Error("github: create-or-join session failed", "error", err, "repo", m.RepoFullName, "pr_number", m.PRNumber)
			// Same reasoning as the parseMention error path above -- this
			// delivery was claimed but never actually acted on, so release
			// the claim to let a genuine redelivery retry.
			if releaseErr := deliveries.Release(ctx, githubDeliveryProvider, deliveryID); releaseErr != nil {
				logger.Error("github: release webhook delivery claim failed", "error", releaseErr, "delivery_id", deliveryID)
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logger.Info("github: mention processed",
			"session_id", session.ID, "turn_id", turn.ID, "new_session", isNew,
			"repo", m.RepoFullName, "pr_number", m.PRNumber, "event_type", eventType)
		w.WriteHeader(http.StatusOK)
	}
}
