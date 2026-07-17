// Command control-plane is the Narvi control plane binary: config, wiring,
// migrations, HTTP+WS server. Config loading + validation landed in PR-02
// (§5.4); structured logging + OTel bootstrap landed in PR-03 (§5.3); the
// real HTTP+WS server lands in PR-06+ (§5.2, §10-P0).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/khazaddev/narvi/internal/platform"
)

func main() {
	cfg, err := platform.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	logger := platform.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	ctx := context.Background()

	shutdownOTel, err := platform.SetupOTel(ctx, "narvi-control-plane")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer func() {
		if err := shutdownOTel(ctx); err != nil {
			slog.Error("otel shutdown failed", "error", err)
		}
	}()

	slog.Info("narvi control-plane: config ok — see PR-06 for the real server",
		"stage", cfg.Stage,
		"log_level", cfg.LogLevel,
	)
}
