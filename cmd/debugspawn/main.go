// Temporary diagnostic program -- to be removed once the fix to
// opencodeproc.waitHealthy (spawn.go) is confirmed on the actual GitHub
// Actions runner. Calls the REAL opencodeproc.Spawn function directly.
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

	start := time.Now()
	result, err := opencodeproc.Spawn(ctx, sup, workDir, 20*time.Second, 250*time.Millisecond)
	fmt.Printf("Spawn took %s, result=%+v err=%v\n", time.Since(start), result, err)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	_ = sup.StopAll(stopCtx, 5*time.Second)

	if err != nil {
		os.Exit(1)
	}
}
