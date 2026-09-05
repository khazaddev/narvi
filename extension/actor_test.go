package extension_test

import (
	"context"
	"testing"

	"github.com/narvidev/narvi/extension"
	"github.com/narvidev/narvi/internal/domain/authz"
	"github.com/narvidev/narvi/internal/platform"
)

// TestActorFromContext_Present proves the alias round-trips a context
// built by platform.WithUser into an extension.Actor carrying the same
// UserID/Role -- the exact construction internal/adapters/inbound/httpapi's
// own helpers.go performs inline, re-exported here for a module that
// cannot import that internal package.
func TestActorFromContext_Present(t *testing.T) {
	ctx := platform.WithUser(context.Background(), platform.AuthenticatedUser{
		ID:   "user-1",
		Role: "admin",
	})

	actor, ok := extension.ActorFromContext(ctx)
	if !ok {
		t.Fatal("ActorFromContext() ok = false, want true")
	}
	want := extension.Actor{UserID: "user-1", Role: authz.Role("admin")}
	if actor != want {
		t.Errorf("ActorFromContext() = %+v, want %+v", actor, want)
	}
}

// TestActorFromContext_Absent proves a context with no authenticated user
// -- e.g. a route reachable without going through RequireAuth first --
// reports ok=false rather than a zero-value Actor a caller might mistake
// for a real, unprivileged one.
func TestActorFromContext_Absent(t *testing.T) {
	actor, ok := extension.ActorFromContext(context.Background())
	if ok {
		t.Errorf("ActorFromContext() ok = true, want false (no user in context)")
	}
	if actor != (extension.Actor{}) {
		t.Errorf("ActorFromContext() = %+v, want the zero Actor when ok = false", actor)
	}
}
