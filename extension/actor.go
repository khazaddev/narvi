// This file (actor.go) re-exports the RBAC actor identity a module's own
// Mount handler needs to authorize a request -- see internal/domain/authz
// for the matrix this identity feeds.

package extension

import (
	"context"

	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// Actor is authz.Actor, re-exported as a type alias -- see that package's
// own doc comment. A module wanting to authorize a request against its
// own rules (or against internal/domain/authz.Authorize, which Actor's
// identity preservation lets it call directly with a value built here)
// uses this, never a hand-rolled identity shape of its own.
type Actor = authz.Actor

// ActorFromContext returns the Actor the current request's own
// authenticated user represents, and whether one was present.
// Mirrors internal/adapters/inbound/httpapi's own helpers.go construction
// of authz.Actor{UserID: ..., Role: ...} from platform.UserFromContext
// exactly -- httpapi is internal and so cannot be called directly from a
// module, and this is that same construction, re-exported.
//
// Present whenever ctx comes from a request Runtime.RequireAuth has
// already gated (every module route, since Mount's own router is mounted
// behind it before a module ever sees it) -- absent otherwise, which a
// module handler should treat as an internal error (a route reachable
// without going through RequireAuth first), never as an anonymous actor.
func ActorFromContext(ctx context.Context) (Actor, bool) {
	u, ok := platform.UserFromContext(ctx)
	if !ok {
		return Actor{}, false
	}
	return Actor{UserID: u.ID, Role: authz.Role(u.Role)}, true
}
