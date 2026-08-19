package seed

import (
	"testing"

	"github.com/khazaddev/narvi/internal/adapters/outbound/postgres/sqlcgen"
)

// TestResolveInitialRole_OnlyInitialAdminEmailsGrantAdmin is a mutation
// guard for §13.4's "everyone defaults to member; initial admins set by
// config" -- the ONLY function in this package that ever decides a role,
// and the ONLY input it may ever consult is initialAdminEmails
// (platform.Config.InitialAdminEmails). If this function (or its call
// site in seedParticipant) were ever changed to also honor something
// participant-supplied -- there is no such field today, see
// seedmanifest's own doc comment -- this test's "not in the admin list"
// case would start failing the moment that participant-supplied value
// were wired in, because this test asserts role=member independent of
// anything about the participant OTHER than whether their email is
// literally present in initialAdminEmails.
func TestResolveInitialRole_OnlyInitialAdminEmailsGrantAdmin(t *testing.T) {
	t.Parallel()

	admins := []string{"admin@example.test", "Second.Admin@Example.test"}

	tests := []struct {
		name  string
		email string
		want  sqlcgen.UserRole
	}{
		{"exact match", "admin@example.test", sqlcgen.UserRoleAdmin},
		{"case-insensitive match", "ADMIN@EXAMPLE.TEST", sqlcgen.UserRoleAdmin},
		{"case-insensitive match, mixed-case list entry", "second.admin@example.test", sqlcgen.UserRoleAdmin},
		{"not in list", "someone-else@example.test", sqlcgen.UserRoleMember},
		{"substring of an admin email is not a match", "admin@example.test.evil.test", sqlcgen.UserRoleMember},
		{"empty email", "", sqlcgen.UserRoleMember},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveInitialRole(tc.email, admins)
			if got != tc.want {
				t.Errorf("resolveInitialRole(%q, %v) = %v, want %v", tc.email, admins, got, tc.want)
			}
		})
	}
}

// TestResolveInitialRole_EmptyAdminListDefaultsEveryoneToMember mirrors
// platform.Config.InitialAdminEmails' own documented "optional -- an
// empty list simply means every first-time sign-in defaults to role
// member" contract.
func TestResolveInitialRole_EmptyAdminListDefaultsEveryoneToMember(t *testing.T) {
	t.Parallel()
	if got := resolveInitialRole("anyone@example.test", nil); got != sqlcgen.UserRoleMember {
		t.Errorf("resolveInitialRole with nil admin list = %v, want member", got)
	}
}
