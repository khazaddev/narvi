package github

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
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
			// A real redelivery of an already-claimed delivery id (GitHub
			// retries on timeout/5xx) -- acknowledge without reprocessing.
			logger.Info("github: duplicate webhook delivery, skipping", "delivery_id", deliveryID)
			w.WriteHeader(http.StatusOK)
			return
		}

		eventType := r.Header.Get("X-GitHub-Event")
		m, ok, err := parseMention(eventType, body, mentionRE)
		if err != nil {
			logger.Error("github: parse webhook payload failed", "error", err, "event_type", eventType, "delivery_id", deliveryID)
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

		session, turn, isNew, err := coalescer.CreateOrJoin(ctx, m.RepoFullName, m.PRNumber, req)
		if err != nil {
			logger.Error("github: create-or-join session failed", "error", err, "repo", m.RepoFullName, "pr_number", m.PRNumber)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		logger.Info("github: mention processed",
			"session_id", session.ID, "turn_id", turn.ID, "new_session", isNew,
			"repo", m.RepoFullName, "pr_number", m.PRNumber, "event_type", eventType)
		w.WriteHeader(http.StatusOK)
	}
}
