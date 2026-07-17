// Command control-plane is the Narvi control plane binary: config, wiring,
// migrations, HTTP+WS server. Config loading + validation landed in PR-02
// (§5.4); the real HTTP+WS server lands in PR-06+ (§5.2, §10-P0).
package main

import (
	"fmt"
	"os"

	"github.com/khazaddev/narvi/internal/platform"
)

func main() {
	cfg, err := platform.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("narvi control-plane: config ok (stage=%s) — see PR-06 for the real server\n", cfg.Stage)
}
