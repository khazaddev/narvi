package authz_test

import (
	"errors"
	"testing"

	"github.com/khazaddev/narvi/internal/domain/authz"
)

// TestMatrix_CoversEveryAction proves every Action in authz.AllActions has
// an entry in the (unexported) matrix -- run indirectly, by proving
// Authorize never returns ErrUnknownAction for any of them, since the
// matrix map itself is unexported and this is an external (_test) package.
// A future Action added to action.go without a matching matrix row fails
// this test immediately, rather than silently defaulting to "forbidden
// for everyone" or panicking at some future call site.
func TestMatrix_CoversEveryAction(t *testing.T) {
	t.Parallel()

	for _, action := range authz.AllActions {
		action := action
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()
			err := authz.Authorize(authz.Actor{Role: authz.RoleAdmin}, action, authz.Resource{})
			if errors.Is(err, authz.ErrUnknownAction) {
				t.Errorf("Authorize(admin, %s, {}) = %v, want a matrix entry (ErrUnknownAction)", action, err)
			}
		})
	}
}

// TestAuthorize_ExhaustiveMatrix is table-driven over every (role, action)
// pair this Step's own §13.3 matrix defines, including the own/joined vs
// any-session distinction for the two actions that carry it
// (ActionPromptSession, ActionApprovePlan) -- exactly the "exhaustive
// tests" the Step brief calls for. Every row of the spec's own table
// (docs/TECHNICAL_PLAN.md §13.3) has a corresponding block below.
func TestAuthorize_ExhaustiveMatrix(t *testing.T) {
	t.Parallel()

	type tc struct {
		name          string
		role          authz.Role
		action        authz.Action
		ownedOrJoined bool
		want          bool
	}

	tests := []tc{
		// Row 1: view sessions/analytics -- ALL FOUR roles, viewer
		// included (read-only, but no separate write verb exists for
		// either action to withhold from viewer).
		{"admin views sessions", authz.RoleAdmin, authz.ActionViewSessions, false, true},
		{"maintainer views sessions", authz.RoleMaintainer, authz.ActionViewSessions, false, true},
		{"member views sessions", authz.RoleMember, authz.ActionViewSessions, false, true},
		{"viewer views sessions", authz.RoleViewer, authz.ActionViewSessions, false, true},
		{"admin views analytics", authz.RoleAdmin, authz.ActionViewAnalytics, false, true},
		{"maintainer views analytics", authz.RoleMaintainer, authz.ActionViewAnalytics, false, true},
		{"member views analytics", authz.RoleMember, authz.ActionViewAnalytics, false, true},
		{"viewer views analytics", authz.RoleViewer, authz.ActionViewAnalytics, false, true},

		// Row 2a: create session -- admin/maintainer/member, viewer
		// never. No ownership concept (new resource) -- OwnedOrJoined is
		// irrelevant, asserted both false and true to prove that.
		{"admin creates session", authz.RoleAdmin, authz.ActionCreateSession, false, true},
		{"maintainer creates session", authz.RoleMaintainer, authz.ActionCreateSession, false, true},
		{"member creates session", authz.RoleMember, authz.ActionCreateSession, false, true},
		{"member creates session (ownedOrJoined irrelevant)", authz.RoleMember, authz.ActionCreateSession, true, true},
		{"viewer cannot create session", authz.RoleViewer, authz.ActionCreateSession, false, false},
		{"viewer cannot create session even if ownedOrJoined", authz.RoleViewer, authz.ActionCreateSession, true, false},

		// Row 2b: prompt a session -- admin/maintainer on ANY session;
		// member ONLY on own/joined; viewer never, regardless.
		{"admin prompts any session", authz.RoleAdmin, authz.ActionPromptSession, false, true},
		{"admin prompts owned session", authz.RoleAdmin, authz.ActionPromptSession, true, true},
		{"maintainer prompts any session", authz.RoleMaintainer, authz.ActionPromptSession, false, true},
		{"maintainer prompts owned session", authz.RoleMaintainer, authz.ActionPromptSession, true, true},
		{"member prompts owned/joined session", authz.RoleMember, authz.ActionPromptSession, true, true},
		{"member cannot prompt a session they neither own nor joined", authz.RoleMember, authz.ActionPromptSession, false, false},
		{"viewer cannot prompt any session", authz.RoleViewer, authz.ActionPromptSession, false, false},
		{"viewer cannot prompt even an owned/joined session", authz.RoleViewer, authz.ActionPromptSession, true, false},

		// Row 2c/Row 3b: approve a plan -- admin/maintainer approve ANY
		// plan; member ONLY on own/joined; viewer never.
		{"admin approves any plan", authz.RoleAdmin, authz.ActionApprovePlan, false, true},
		{"admin approves owned plan", authz.RoleAdmin, authz.ActionApprovePlan, true, true},
		{"maintainer approves any plan", authz.RoleMaintainer, authz.ActionApprovePlan, false, true},
		{"maintainer approves owned plan", authz.RoleMaintainer, authz.ActionApprovePlan, true, true},
		{"member approves owned/joined plan", authz.RoleMember, authz.ActionApprovePlan, true, true},
		{"member cannot approve a plan on a session they neither own nor joined", authz.RoleMember, authz.ActionApprovePlan, false, false},
		{"viewer cannot approve any plan", authz.RoleViewer, authz.ActionApprovePlan, false, false},
		{"viewer cannot approve even an owned/joined plan", authz.RoleViewer, authz.ActionApprovePlan, true, false},

		// Row 3a: stop/resume ANY session -- admin/maintainer ONLY. No
		// member own/joined escape hatch at all, unlike prompt/approve
		// above -- asserted with ownedOrJoined=true too, to prove the
		// carve-out genuinely does not exist for this action.
		{"admin stops any session", authz.RoleAdmin, authz.ActionStopSession, false, true},
		{"maintainer stops any session", authz.RoleMaintainer, authz.ActionStopSession, false, true},
		{"member cannot stop a session they do not own", authz.RoleMember, authz.ActionStopSession, false, false},
		{"member cannot stop even a session they own/joined", authz.RoleMember, authz.ActionStopSession, true, false},
		{"viewer cannot stop any session", authz.RoleViewer, authz.ActionStopSession, false, false},
		{"admin resumes any session", authz.RoleAdmin, authz.ActionResumeSession, false, true},
		{"maintainer resumes any session", authz.RoleMaintainer, authz.ActionResumeSession, false, true},
		{"member cannot resume a session they do not own", authz.RoleMember, authz.ActionResumeSession, false, false},
		{"member cannot resume even a session they own/joined", authz.RoleMember, authz.ActionResumeSession, true, false},
		{"viewer cannot resume any session", authz.RoleViewer, authz.ActionResumeSession, false, false},

		// Row 4: automations/environments/repo+env secrets --
		// admin/maintainer only.
		{"admin manages automations", authz.RoleAdmin, authz.ActionManageAutomations, false, true},
		{"maintainer manages automations", authz.RoleMaintainer, authz.ActionManageAutomations, false, true},
		{"member cannot manage automations", authz.RoleMember, authz.ActionManageAutomations, false, false},
		{"member cannot manage automations even if ownedOrJoined", authz.RoleMember, authz.ActionManageAutomations, true, false},
		{"viewer cannot manage automations", authz.RoleViewer, authz.ActionManageAutomations, false, false},
		{"admin manages environments", authz.RoleAdmin, authz.ActionManageEnvironments, false, true},
		{"maintainer manages environments", authz.RoleMaintainer, authz.ActionManageEnvironments, false, true},
		{"member cannot manage environments", authz.RoleMember, authz.ActionManageEnvironments, false, false},
		{"viewer cannot manage environments", authz.RoleViewer, authz.ActionManageEnvironments, false, false},
		{"admin manages repo secrets", authz.RoleAdmin, authz.ActionManageRepoSecrets, false, true},
		{"maintainer manages repo secrets", authz.RoleMaintainer, authz.ActionManageRepoSecrets, false, true},
		{"member cannot manage repo secrets", authz.RoleMember, authz.ActionManageRepoSecrets, false, false},
		{"viewer cannot manage repo secrets", authz.RoleViewer, authz.ActionManageRepoSecrets, false, false},
		{"admin manages env secrets", authz.RoleAdmin, authz.ActionManageEnvSecrets, false, true},
		{"maintainer manages env secrets", authz.RoleMaintainer, authz.ActionManageEnvSecrets, false, true},
		{"member cannot manage env secrets", authz.RoleMember, authz.ActionManageEnvSecrets, false, false},
		{"viewer cannot manage env secrets", authz.RoleViewer, authz.ActionManageEnvSecrets, false, false},

		// Row 5: review verdicts/re-trigger/auto-approve config --
		// admin/maintainer only.
		{"admin edits review verdict", authz.RoleAdmin, authz.ActionEditReviewVerdict, false, true},
		{"maintainer edits review verdict", authz.RoleMaintainer, authz.ActionEditReviewVerdict, false, true},
		{"member cannot edit review verdict", authz.RoleMember, authz.ActionEditReviewVerdict, false, false},
		{"viewer cannot edit review verdict", authz.RoleViewer, authz.ActionEditReviewVerdict, false, false},
		{"admin retriggers review", authz.RoleAdmin, authz.ActionRetriggerReview, false, true},
		{"maintainer retriggers review", authz.RoleMaintainer, authz.ActionRetriggerReview, false, true},
		{"member cannot retrigger review", authz.RoleMember, authz.ActionRetriggerReview, false, false},
		{"viewer cannot retrigger review", authz.RoleViewer, authz.ActionRetriggerReview, false, false},
		{"admin configures auto-approve", authz.RoleAdmin, authz.ActionConfigureAutoApprove, false, true},
		{"maintainer configures auto-approve", authz.RoleMaintainer, authz.ActionConfigureAutoApprove, false, true},
		{"member cannot configure auto-approve", authz.RoleMember, authz.ActionConfigureAutoApprove, false, false},
		{"viewer cannot configure auto-approve", authz.RoleViewer, authz.ActionConfigureAutoApprove, false, false},

		// Row 6: integrations/global secrets/template activation/members
		// & roles/sentinel toggle -- admin ONLY, not even maintainer.
		{"admin manages integrations", authz.RoleAdmin, authz.ActionManageIntegrations, false, true},
		{"maintainer cannot manage integrations", authz.RoleMaintainer, authz.ActionManageIntegrations, false, false},
		{"member cannot manage integrations", authz.RoleMember, authz.ActionManageIntegrations, false, false},
		{"viewer cannot manage integrations", authz.RoleViewer, authz.ActionManageIntegrations, false, false},
		{"admin manages global secrets", authz.RoleAdmin, authz.ActionManageGlobalSecrets, false, true},
		{"maintainer cannot manage global secrets", authz.RoleMaintainer, authz.ActionManageGlobalSecrets, false, false},
		{"member cannot manage global secrets", authz.RoleMember, authz.ActionManageGlobalSecrets, false, false},
		{"viewer cannot manage global secrets", authz.RoleViewer, authz.ActionManageGlobalSecrets, false, false},
		{"admin activates prompt template", authz.RoleAdmin, authz.ActionActivatePromptTemplate, false, true},
		{"maintainer cannot activate prompt template", authz.RoleMaintainer, authz.ActionActivatePromptTemplate, false, false},
		{"member cannot activate prompt template", authz.RoleMember, authz.ActionActivatePromptTemplate, false, false},
		{"viewer cannot activate prompt template", authz.RoleViewer, authz.ActionActivatePromptTemplate, false, false},
		{"admin manages members", authz.RoleAdmin, authz.ActionManageMembers, false, true},
		{"maintainer cannot manage members", authz.RoleMaintainer, authz.ActionManageMembers, false, false},
		{"member cannot manage members", authz.RoleMember, authz.ActionManageMembers, false, false},
		{"viewer cannot manage members", authz.RoleViewer, authz.ActionManageMembers, false, false},
		{"admin toggles sentinel auto-fix", authz.RoleAdmin, authz.ActionToggleSentinelAutoFix, false, true},
		{"maintainer cannot toggle sentinel auto-fix", authz.RoleMaintainer, authz.ActionToggleSentinelAutoFix, false, false},
		{"member cannot toggle sentinel auto-fix", authz.RoleMember, authz.ActionToggleSentinelAutoFix, false, false},
		{"viewer cannot toggle sentinel auto-fix", authz.RoleViewer, authz.ActionToggleSentinelAutoFix, false, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			actor := authz.Actor{UserID: "u1", Role: tc.role}
			resource := authz.Resource{OwnedOrJoined: tc.ownedOrJoined}
			err := authz.Authorize(actor, tc.action, resource)

			if tc.want {
				if err != nil {
					t.Fatalf("Authorize(%s, %s, ownedOrJoined=%v) = %v, want nil", tc.role, tc.action, tc.ownedOrJoined, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("Authorize(%s, %s, ownedOrJoined=%v) = nil, want an error", tc.role, tc.action, tc.ownedOrJoined)
			}
			if !errors.Is(err, authz.ErrForbidden) {
				t.Errorf("Authorize(%s, %s, ownedOrJoined=%v) error = %v, want errors.Is(err, ErrForbidden)", tc.role, tc.action, tc.ownedOrJoined, err)
			}
			var forbidden *authz.ForbiddenError
			if !errors.As(err, &forbidden) {
				t.Fatalf("Authorize(%s, %s, ownedOrJoined=%v) error = %v, want *ForbiddenError", tc.role, tc.action, tc.ownedOrJoined, err)
			}
			if forbidden.Actor != actor || forbidden.Action != tc.action {
				t.Errorf("ForbiddenError = %+v, want Actor=%+v Action=%s", forbidden, actor, tc.action)
			}
			if forbidden.Error() == "" {
				t.Error("ForbiddenError.Error() is empty")
			}
		})
	}
}

// TestAuthorize_UnknownAction proves an Action absent from the matrix
// (never true for any of authz.AllActions, per TestMatrix_CoversEveryAction
// above, but a real possibility for a caller's own typo/stale constant)
// gets ErrUnknownAction, distinct from ErrForbidden.
func TestAuthorize_UnknownAction(t *testing.T) {
	t.Parallel()

	err := authz.Authorize(authz.Actor{Role: authz.RoleAdmin}, authz.Action("bogus_action"), authz.Resource{})
	if err == nil {
		t.Fatal("Authorize with an unknown action = nil, want an error")
	}
	if !errors.Is(err, authz.ErrUnknownAction) {
		t.Errorf("error = %v, want errors.Is(err, ErrUnknownAction)", err)
	}
	if errors.Is(err, authz.ErrForbidden) {
		t.Error("an unknown action must never also satisfy errors.Is(err, ErrForbidden) -- callers distinguish 403 from a caller bug")
	}
}

// TestAllRoles_MatchesRoleConstants is a small sanity check that AllRoles
// has exactly the four roles the matrix and every call site rely on, no
// more, no fewer -- a silent addition/removal here would invalidate this
// package's own "exhaustive" claim above without failing anything else.
func TestAllRoles_MatchesRoleConstants(t *testing.T) {
	t.Parallel()

	want := []authz.Role{authz.RoleAdmin, authz.RoleMaintainer, authz.RoleMember, authz.RoleViewer}
	if len(authz.AllRoles) != len(want) {
		t.Fatalf("len(AllRoles) = %d, want %d", len(authz.AllRoles), len(want))
	}
	for i, r := range want {
		if authz.AllRoles[i] != r {
			t.Errorf("AllRoles[%d] = %s, want %s", i, authz.AllRoles[i], r)
		}
	}
}
