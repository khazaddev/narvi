package boot

import (
	"context"
	"os/exec"
)

// CheckDeps stands for the real sandbox agent's own legitimate subprocess
// use -- must NOT be reported.
func CheckDeps(ctx context.Context) error {
	return exec.CommandContext(ctx, "true").Run()
}
