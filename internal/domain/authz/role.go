package authz

// Role is one of the four global RBAC roles §13.3 defines, one per user
// (never per-session, never per-resource) — mirrors sqlcgen.UserRole/the
// user_role Postgres enum (migrations/000002_users.up.sql) by value only;
// this package never imports that generated type (§11: domain packages
// stay adapter-independent — see internal/domain/plan's own identical
// "plain types, never sqlcgen.*" precedent). Callers convert at the
// boundary (e.g. authz.Role(userRow.Role)).
type Role string

// The four roles, ordered admin > maintainer > member > viewer (§13.3's
// own ordering) — though Authorize never relies on that ordering being
// numeric/comparable; every row of the matrix is spelled out explicitly
// as its own exact set of allowed roles, not derived from a rank
// comparison, so a future role inserted anywhere in the hierarchy can
// never silently inherit permissions it wasn't explicitly granted.
const (
	RoleAdmin      Role = "admin"
	RoleMaintainer Role = "maintainer"
	RoleMember     Role = "member"
	RoleViewer     Role = "viewer"
)

// AllRoles is every recognized Role, in the matrix's own display order —
// exported so tests (in this package and its callers) can exhaustively
// range over every role without hand-maintaining a second list that could
// drift from the one above.
var AllRoles = []Role{RoleAdmin, RoleMaintainer, RoleMember, RoleViewer}
