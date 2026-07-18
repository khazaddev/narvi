// Temporary diagnostic program -- to be removed once the real cause of
// the opencode-serve health-check timeout on CI is found. Calls the REAL
// opencodeproc.Spawn function directly (not a hand-copied reimplementation
// -- every hand-copied variant tested so far has passed on the actual
// runner, so this removes any chance the reimplementation itself quietly
// differs from the real code in some unnoticed way).
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

func main() {
	workDir, err := os.MkdirTemp("", "debugspawn-")
	if err != nil {
		fmt.Println("MkdirTemp error:", err)
		os.Exit(1)
	}
	fmt.Println("workDir:", workDir)

	sup := supervisor.New()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := opencodeproc.Spawn(ctx, sup, workDir, 20*time.Second, 250*time.Millisecond)
	fmt.Println("Spawn result:", result, "err:", err)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = sup.StopAll(stopCtx, 5*time.Second)

	if err != nil {
		os.Exit(1)
	}
}
