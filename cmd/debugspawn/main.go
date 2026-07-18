// Temporary diagnostic program -- to be removed once the real cause of
// the opencode-serve health-check timeout on CI is found. Reimplements
// opencodeproc.waitHealthy EXACTLY (same shared long-lived context across
// every poll attempt, same 250ms poll interval -- the one thing every
// earlier hand-copied scenario got WRONG was giving each health-check
// attempt its own short-lived context, unlike the real code, which shares
// ONE context across the whole polling loop), with per-attempt tracing
// added so the actual failure mode (refused vs. hang vs. something else)
// is directly visible instead of inferred.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

func healthCheck(ctx context.Context, baseURL string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/health", nil)
	if err != nil {
		return false, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices, nil
}

func main() {
	sup := supervisor.New()

	proc, err := sup.Spawn(supervisor.Spec{
		Path: "opencode",
		Args: []string{"serve", "--port", "5555", "--hostname", "127.0.0.1"},
	})
	if err != nil {
		fmt.Println("spawn error:", err)
		os.Exit(1)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.StopAll(stopCtx, 5*time.Second)
	}()

	baseURL := "http://127.0.0.1:5555"

	readyCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	attempt := 0
	start := time.Now()
	for {
		attempt++
		if result, exited := proc.Exited(); exited {
			fmt.Printf("[t=%s attempt=%d] process already exited: %+v\n", time.Since(start), attempt, result)
			os.Exit(1)
		}

		ok, herr := healthCheck(readyCtx, baseURL)
		elapsed := time.Since(start)
		if ok {
			fmt.Printf("[t=%s attempt=%d] HEALTHY\n", elapsed, attempt)
			os.Exit(0)
		}
		// Only print every 10th attempt plus the first few, to avoid
		// flooding the log across up to ~80 attempts in 20s.
		if attempt <= 5 || attempt%10 == 0 {
			fmt.Printf("[t=%s attempt=%d] not healthy, err=%v\n", elapsed, attempt, herr)
		}

		select {
		case <-readyCtx.Done():
			fmt.Printf("[t=%s attempt=%d] TIMED OUT, ctx err=%v\n", time.Since(start), attempt, readyCtx.Err())
			os.Exit(1)
		case <-ticker.C:
		}
	}
}
