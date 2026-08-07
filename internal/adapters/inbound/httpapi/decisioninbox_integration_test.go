//go:build integration

// Integration tests for ListDecisionInbox/MergePullRequest (Step 60,
// "decision inbox: read model + API", §16) against a REAL Postgres
// instance -- gated behind the "integration" build tag. Deliberately a
// SELF-CONTAINED router (auth.Middleware + these two routes only) rather
// than extending this package's own shared testRig (httpapi_integration_
// test.go): testRig is a large, heavily-shared fixture many other tests
// already depend on, and these two handlers need nothing from it beyond
// the SAME shared Postgres pool (newTestPool, already in scope in this
// package) and the SAME auth.Middleware every other route already uses.
package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/khazaddev/narvi/contracts/gen/go/restdtos"
	"github.com/khazaddev/narvi/internal/adapters/inbound/auth"
	"github.com/khazaddev/narvi/internal/adapters/inbound/httpapi"
	narvipg "github.com/khazaddev/narvi/internal/adapters/outbound/postgres"
	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
	"github.com/khazaddev/narvi/internal/app/decisioninbox"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/platform"
)

// decisionInboxTokenKey is a fixed, valid 32-byte AES key -- mirrors
// httpapi_integration_test.go's own tokenEncryptionKey literal exactly
// ("01234567890123456789012345678901", 32 bytes).
var decisionInboxTokenKey = []byte("01234567890123456789012345678901")

// decisionInboxTestRig is this file's own small, self-contained fixture --
// see this file's own top doc comment for why it does not reuse testRig.
type decisionInboxTestRig struct {
	pool         *pgxpool.Pool
	users        *narvipg.UserStore
	userSessions *narvipg.UserSessionStore
	identities   *narvipg.IdentityStore
	sessions     *narvipg.SessionStore
	artifacts    *narvipg.ArtifactStore
	server       *httptest.Server
}

// fakeMergeSourceControl is a minimal test-only ports.SourceControl,
// configurable per test -- mirrors reviewfindings_integration_test.go's
// own applySuggestionFakeSourceControl precedent, narrowed to exactly the
// two methods decisioninbox.RevalidateForMerge/Build actually call
// (ListOpenPRsForUser, MergePR); every other method returns a plain "not
// implemented" error.
type fakeMergeSourceControl struct {
	openPRs    []ports.OpenPR
	mergeSHA   string
	mergeErr   error
	mergeCalls []ports.MergePRSpec
}

var _ ports.SourceControl = (*fakeMergeSourceControl)(nil)

func (f *fakeMergeSourceControl) ListOpenPRsForUser(context.Context, ports.ListOpenPRsForUserSpec) ([]ports.OpenPR, error) {
	return f.openPRs, nil
}
func (f *fakeMergeSourceControl) ResolveCodeOwners(context.Context, ports.ResolveCodeOwnersSpec) ([]ports.Owner, error) {
	return nil, nil
}
func (f *fakeMergeSourceControl) MergePR(_ context.Context, spec ports.MergePRSpec) (string, error) {
	f.mergeCalls = append(f.mergeCalls, spec)
	if f.mergeErr != nil {
		return "", f.mergeErr
	}
	return f.mergeSHA, nil
}
func (f *fakeMergeSourceControl) CreatePR(context.Context, ports.CreatePRSpec) (ports.PRRef, error) {
	return ports.PRRef{}, errors.New("not implemented")
}
func (f *fakeMergeSourceControl) ResolveBranchSHA(context.Context, ports.ResolveBranchSHASpec) (string, string, error) {
	return "", "", errors.New("not implemented")
}
func (f *fakeMergeSourceControl) ResolveContractsFingerprint(context.Context, ports.ResolveContractsFingerprintSpec) (string, bool, error) {
	return "", false, errors.New("not implemented")
}
func (f *fakeMergeSourceControl) CheckRepoAccess(context.Context, ports.CheckRepoAccessSpec) (bool, error) {
	return false, errors.New("not implemented")
}
func (f *fakeMergeSourceControl) GetFileContent(context.Context, ports.GetFileContentSpec) (string, string, bool, error) {
	return "", "", false, errors.New("not implemented")
}
func (f *fakeMergeSourceControl) UpdateFileContent(context.Context, ports.UpdateFileContentSpec) (string, error) {
	return "", errors.New("not implemented")
}
func (f *fakeMergeSourceControl) RegisterPRStack(context.Context, ports.RegisterPRStackSpec) error {
	return errors.New("not implemented")
}
func (f *fakeMergeSourceControl) ListMergedBetween(context.Context, ports.ListMergedBetweenSpec) ([]ports.MergedPR, bool, error) {
	return nil, false, errors.New("not implemented")
}
func (f *fakeMergeSourceControl) CreateBranch(context.Context, ports.CreateBranchSpec) error {
	return errors.New("not implemented")
}

func newDecisionInboxTestRig(t *testing.T, sourceControl ports.SourceControl) *decisionInboxTestRig {
	t.Helper()
	pool := newTestPool(t)

	users := narvipg.NewUserStore(pool)
	userSessions := narvipg.NewUserSessionStore(pool)
	identities := narvipg.NewIdentityStore(pool)
	sessions := narvipg.NewSessionStore(pool)
	artifacts := narvipg.NewArtifactStore(pool)

	deps := decisioninbox.Deps{
		Plans:              narvipg.NewPlanStore(pool),
		Sessions:           sessions,
		Participants:       narvipg.NewParticipantStore(pool),
		Automations:        narvipg.NewAutomationStore(pool),
		Outbox:             narvipg.NewOutboxStore(pool),
		ReviewFindings:     narvipg.NewReviewFindingStore(pool),
		SentinelFixes:      narvipg.NewSentinelFixStore(pool),
		Artifacts:          artifacts,
		Identities:         identities,
		SCMCache:           decisioninbox.NewSCMCache(sourceControl, platform.DefaultTimeouts()),
		TokenEncryptionKey: decisionInboxTokenKey,
		Timeouts:           platform.DefaultTimeouts(),
	}
	auditLog := narvipg.NewAuditLogStore(pool)

	router := chi.NewRouter()
	router.Route("/api/decision-inbox", func(r chi.Router) {
		r.Use(auth.Middleware(userSessions, users))
		r.Get("/", httpapi.ListDecisionInbox(deps))
		r.Post("/merge", httpapi.MergePullRequest(deps, sourceControl, auditLog))
	})

	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	return &decisionInboxTestRig{
		pool: pool, users: users, userSessions: userSessions, identities: identities,
		sessions: sessions, artifacts: artifacts, server: server,
	}
}

// createAuthenticatedUser mirrors testRig.createAuthenticatedUser
// (httpapi_integration_test.go) exactly, duplicated here since this file
// deliberately does not construct a full testRig (this file's own top doc
// comment).
func (rig *decisionInboxTestRig) createAuthenticatedUser(ctx context.Context, t *testing.T, role sqlcgen.UserRole) (sqlcgen.User, string) {
	t.Helper()
	unique := fmt.Sprintf("decisioninbox-test-%d", time.Now().UnixNano())
	user, err := rig.users.Create(ctx, sqlcgen.CreateUserParams{PrimaryEmail: unique + "@example.com", DisplayName: "Test User", Role: role})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	token, err := platform.GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if _, err := rig.userSessions.Create(ctx, sqlcgen.CreateUserSessionParams{
		UserID:    user.ID,
		TokenHash: platform.HashToken(token),
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(platform.DefaultTimeouts().UserSessionTTL), Valid: true},
	}); err != nil {
		t.Fatalf("create user session: %v", err)
	}
	return user, token
}

func (rig *decisionInboxTestRig) linkGitHub(ctx context.Context, t *testing.T, userID pgtype.UUID, externalID string) {
	t.Helper()
	encrypted, err := platform.EncryptToken(decisionInboxTokenKey, []byte("fake-gh-token"))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	if _, err := rig.identities.Create(ctx, sqlcgen.CreateIdentityParams{
		UserID: userID, Provider: sqlcgen.IdentityProviderGithub, ExternalID: externalID,
		EmailVerified: true, LinkedVia: sqlcgen.IdentityLinkedViaAutoEmail, AccessTokenEncrypted: encrypted,
	}); err != nil {
		t.Fatalf("create identity: %v", err)
	}
}

// markPlatformAuthored records a 'pr'-typed artifact for htmlURL, backed
// by a freshly created session -- the durable signal isPlatformAuthored
// (internal/app/decisioninbox) checks before ever classifying a PR
// ready_to_merge.
func (rig *decisionInboxTestRig) markPlatformAuthored(ctx context.Context, t *testing.T, createdBy pgtype.UUID, htmlURL string) {
	t.Helper()
	session, err := rig.sessions.Create(ctx, sqlcgen.CreateSessionParams{SpawnSource: sqlcgen.SessionSpawnSourceGithub, CreatedBy: createdBy})
	if err != nil {
		t.Fatalf("create platform session: %v", err)
	}
	if _, err := rig.artifacts.Create(ctx, sqlcgen.CreateArtifactParams{
		SessionID: session.ID, Type: sqlcgen.ArtifactTypePr, Url: htmlURL, Metadata: []byte("{}"),
	}); err != nil {
		t.Fatalf("create pr artifact: %v", err)
	}
}

func (rig *decisionInboxTestRig) doJSON(t *testing.T, method, path string, body []byte, v any, token string) int {
	t.Helper()
	req, err := http.NewRequest(method, rig.server.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.AddCookie(&http.Cookie{Name: platform.AuthSessionCookieName, Value: token})
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
	return resp.StatusCode
}

func TestListDecisionInbox_RequiresAuth(t *testing.T) {
	rig := newDecisionInboxTestRig(t, &fakeMergeSourceControl{})

	status := rig.doJSON(t, http.MethodGet, "/api/decision-inbox", nil, nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", status, http.StatusUnauthorized)
	}
}

func TestListDecisionInbox_EmptyForFreshUser(t *testing.T) {
	rig := newDecisionInboxTestRig(t, &fakeMergeSourceControl{})
	ctx := context.Background()
	_, token := rig.createAuthenticatedUser(ctx, t, sqlcgen.UserRoleMember)

	var got restdtos.ListDecisionInboxResponse
	status := rig.doJSON(t, http.MethodGet, "/api/decision-inbox", nil, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if len(got.Items) != 0 {
		t.Errorf("Items = %+v, want empty for a fresh user with no linked GitHub identity and no plans", got.Items)
	}
	if got.ScmAsOf != nil {
		t.Errorf("ScmAsOf = %v, want nil (no linked GitHub identity)", *got.ScmAsOf)
	}
	if got.DecisionLatencyComputed {
		t.Error("DecisionLatencyComputed = true, want false (no decisions in the window yet)")
	}
}

func TestMergePullRequest_HappyPath(t *testing.T) {
	const htmlURL = "https://github.com/acme/widgets/pull/1204"
	fakeSCM := &fakeMergeSourceControl{
		openPRs: []ports.OpenPR{
			{
				Owner: "acme", Repo: "widgets", Number: 1204, Title: "low risk", HTMLURL: htmlURL,
				HeadSHA: "headsha1204", Assignees: []ports.PRPerson{{ExternalID: "9001", Login: "octocat"}},
				CIConclusion: ports.CIConclusionSuccess, Labels: []string{"review:low-risk"},
			},
		},
		mergeSHA: "merged-sha-abc",
	}
	rig := newDecisionInboxTestRig(t, fakeSCM)
	ctx := context.Background()

	user, token := rig.createAuthenticatedUser(ctx, t, sqlcgen.UserRoleMember)
	rig.linkGitHub(ctx, t, user.ID, "9001")
	rig.markPlatformAuthored(ctx, t, user.ID, htmlURL)

	body, err := json.Marshal(restdtos.MergePullRequestRequest{RepoFullName: "acme/widgets", PrNumber: 1204})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var got restdtos.MergePullRequestResponse
	status := rig.doJSON(t, http.MethodPost, "/api/decision-inbox/merge", body, &got, token)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want %d", status, http.StatusOK)
	}
	if !got.Merged || got.MergeCommitSha != "merged-sha-abc" {
		t.Errorf("response = %+v, want a successful merge with the fake's own sha", got)
	}
	if len(fakeSCM.mergeCalls) != 1 || fakeSCM.mergeCalls[0].HeadSHA != "headsha1204" {
		t.Errorf("MergePR calls = %+v, want exactly one call with HeadSHA=headsha1204 (the freshly revalidated head, never a stale/client-supplied one)", fakeSCM.mergeCalls)
	}
}

func TestMergePullRequest_NotAssignedToCaller(t *testing.T) {
	fakeSCM := &fakeMergeSourceControl{} // no open PRs for anyone
	rig := newDecisionInboxTestRig(t, fakeSCM)
	ctx := context.Background()

	user, token := rig.createAuthenticatedUser(ctx, t, sqlcgen.UserRoleMember)
	rig.linkGitHub(ctx, t, user.ID, "9001")

	body, err := json.Marshal(restdtos.MergePullRequestRequest{RepoFullName: "acme/widgets", PrNumber: 999})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	var errBody map[string]string
	status := rig.doJSON(t, http.MethodPost, "/api/decision-inbox/merge", body, &errBody, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body: %+v)", status, http.StatusConflict, errBody)
	}
	if len(fakeSCM.mergeCalls) != 0 {
		t.Errorf("MergePR called %d times, want 0 -- revalidation must reject before ever calling MergePR", len(fakeSCM.mergeCalls))
	}
}

func TestMergePullRequest_NotPlatformAuthored(t *testing.T) {
	const htmlURL = "https://github.com/acme/widgets/pull/1205"
	fakeSCM := &fakeMergeSourceControl{
		openPRs: []ports.OpenPR{
			{
				Owner: "acme", Repo: "widgets", Number: 1205, Title: "human PR", HTMLURL: htmlURL,
				HeadSHA: "headsha1205", Assignees: []ports.PRPerson{{ExternalID: "9001", Login: "octocat"}},
				CIConclusion: ports.CIConclusionSuccess, Labels: []string{"review:low-risk"},
			},
		},
	}
	rig := newDecisionInboxTestRig(t, fakeSCM)
	ctx := context.Background()

	user, token := rig.createAuthenticatedUser(ctx, t, sqlcgen.UserRoleMember)
	rig.linkGitHub(ctx, t, user.ID, "9001")
	// Deliberately NO markPlatformAuthored call -- this PR was not opened
	// by any Narvi session.

	body, err := json.Marshal(restdtos.MergePullRequestRequest{RepoFullName: "acme/widgets", PrNumber: 1205})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	status := rig.doJSON(t, http.MethodPost, "/api/decision-inbox/merge", body, nil, token)
	if status != http.StatusConflict {
		t.Fatalf("status = %d, want %d", status, http.StatusConflict)
	}
	if len(fakeSCM.mergeCalls) != 0 {
		t.Errorf("MergePR called %d times, want 0", len(fakeSCM.mergeCalls))
	}
}
