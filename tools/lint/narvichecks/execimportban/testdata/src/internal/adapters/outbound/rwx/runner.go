package rwx

import (
	"context"
	"os/exec"
)

// Run stands for the real rwx runner's own legitimate subprocess use --
// must NOT be reported.
func Run(ctx context.Context) error {
	return exec.CommandContext(ctx, "true").Run()
}
