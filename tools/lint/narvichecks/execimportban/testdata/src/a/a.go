package a

import (
	"context"
	"os/exec" // want `"os/exec" may only be imported from internal/adapters/outbound, internal/sandboxagent, or cmd/sandbox-agent`
)

// runSomething is the mistake this analyzer exists to catch: an
// ingress-adjacent package spawning a subprocess of its own.
func runSomething(ctx context.Context) error {
	return exec.CommandContext(ctx, "echo", "hi").Run()
}
