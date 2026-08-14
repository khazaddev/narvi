package boot_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
)

// freePort binds to 127.0.0.1:0, reads back the actual ephemeral port, and
// immediately closes the listener, exactly like
// internal/sandboxagent/services's own test helper of the same name (a
// separate package, so duplicated rather than shared -- these are test
// files, not production code).
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// writeServicesManifest writes a .narvi/services.yml under repoDir with
// the given raw content.
func writeServicesManifest(t *testing.T, repoDir, content string) {
	t.Helper()

	dir := filepath.Join(repoDir, ".narvi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "services.yml"), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// fastReadyManifestYAML is a one-service manifest whose service opens a
// real TCP listener on port almost immediately -- a YAML block scalar (|)
// is used for cmd specifically so the embedded shell/python quoting needs
// no YAML-level escaping at all.
func fastReadyManifestYAML(name string, port int, criticality string) string {
	return fmt.Sprintf(`
services:
  - name: %s
    cmd: |
      python3 -c "import socket;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind(('127.0.0.1', %d));s.listen(1);import time;time.sleep(30)"
    readiness: { port: %d }
    criticality: %s
`, name, port, port, criticality)
}

// crashingManifestYAML is a one-service manifest whose service exits
// immediately -- used to exercise the fatal (primary) service-failure
// path.
func crashingManifestYAML(name string, port int, criticality string) string {
	return fmt.Sprintf(`
services:
  - name: %s
    cmd: "exit 1"
    readiness: { port: %d }
    criticality: %s
`, name, port, criticality)
}

func noopReporter(services.BootProgressEvent) {}

// collectingReporter is a ProgressReporter that records every event
// received, safe for concurrent use.
type collectingReporter struct {
	mu     sync.Mutex
	events []services.BootProgressEvent
}

func (c *collectingReporter) report(e services.BootProgressEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
}

func (c *collectingReporter) hasPhase(name string, phase services.Phase) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.ServiceName == name && e.Phase == phase {
			return true
		}
	}
	return false
}

const (
	testReadinessTimeout      = 5 * time.Second
	testReadinessPollInterval = 30 * time.Millisecond
)

// TestRunBoot_MixedManifestAndHookFallback proves a single RunBoot call
// correctly processes one repo via services.yml supervision and a
// SIBLING repo (same call) via the classic setup.sh/start.sh fallback,
// exactly as §14.2 requires ("backward compatible, no forced migration").
func TestRunBoot_MixedManifestAndHookFallback(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()

	// repo-a: a services.yml-driven repo.
	repoADir := filepath.Join(workspaceDir, "repo-a")
	if err := os.MkdirAll(repoADir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	port := freePort(t)
	writeServicesManifest(t, repoADir, fastReadyManifestYAML("web", port, "primary"))

	// repo-b: no manifest at all -- falls back to start.sh.
	repoBMarker := filepath.Join(workspaceDir, "repo-b-marker")
	writeScript(t, filepath.Join(workspaceDir, "repo-b", "start.sh"), "touch "+repoBMarker)

	sup := supervisor.New()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.StopAll(ctx, time.Second)
	})

	reporter := &collectingReporter{}
	repos := []boot.RepoInfo{
		{Name: "repo-a", Primary: true},
		{Name: "repo-b", Primary: false},
	}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, reporter.report, 5*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil", err)
	}

	assertFileExists(t, repoBMarker)

	if !reporter.hasPhase("web", services.PhaseReady) {
		t.Errorf("reporter never observed PhaseReady for repo-a's %q service; events: %+v", "web", reporter.events)
	}
}

// TestRunBoot_AbsentManifestFallsBackToHooks proves a single repo with no
// .narvi/services.yml at all still runs its start.sh via the classic hook
// path, unchanged from Step 13's own behavior.
func TestRunBoot_AbsentManifestFallsBackToHooks(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	marker := filepath.Join(workspaceDir, "marker-start")
	writeScript(t, filepath.Join(workspaceDir, "repo-a", "start.sh"), "touch "+marker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, noopReporter, 5*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err != nil {
		t.Fatalf("RunBoot() error = %v, want nil", err)
	}

	assertFileExists(t, marker)
}

// TestRunBoot_MalformedManifestIsAFatalError proves a present-but-invalid
// services.yml (here: an empty services list, per
// servicemanifest.EmptyServicesError) is a real, propagated error -- NOT
// silently treated as absent and falling back to hooks. A start.sh sitting
// right next to the malformed manifest must never run.
func TestRunBoot_MalformedManifestIsAFatalError(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()
	repoDir := filepath.Join(workspaceDir, "repo-a")
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeServicesManifest(t, repoDir, "services: []\n")

	wouldRunMarker := filepath.Join(workspaceDir, "would-run-marker")
	writeScript(t, filepath.Join(repoDir, "start.sh"), "touch "+wouldRunMarker)

	sup := supervisor.New()
	repos := []boot.RepoInfo{{Name: "repo-a", Primary: true}}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, noopReporter, 5*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err == nil {
		t.Fatal("RunBoot() error = nil, want an error for a malformed services.yml")
	}

	assertFileAbsent(t, wouldRunMarker)
}

// TestRunBoot_FatalFailureInRepoAStopsBeforeRepoB proves a fatal failure
// (a primary service that crashes before ever becoming ready) in repo-a
// stops RunBoot immediately -- repo-b, in the SAME call, is never even
// attempted. Uses the same marker-file technique as
// TestRunHooks_FatalFailureStopsImmediately.
func TestRunBoot_FatalFailureInRepoAStopsBeforeRepoB(t *testing.T) {
	t.Parallel()

	workspaceDir := t.TempDir()

	repoADir := filepath.Join(workspaceDir, "repo-a")
	if err := os.MkdirAll(repoADir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeServicesManifest(t, repoADir, crashingManifestYAML("crashes", freePort(t), "primary"))

	laterMarker := filepath.Join(workspaceDir, "repo-b-marker")
	writeScript(t, filepath.Join(workspaceDir, "repo-b", "start.sh"), "touch "+laterMarker)

	sup := supervisor.New()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = sup.StopAll(ctx, time.Second)
	})

	repos := []boot.RepoInfo{
		{Name: "repo-a", Primary: true},
		{Name: "repo-b", Primary: false},
	}

	err := boot.RunBoot(context.Background(), sup, workspaceDir, repos, sandboxboot.BootModeFresh, nil,
		nil, noopReporter, 5*time.Second, time.Second, testReadinessTimeout, testReadinessPollInterval)
	if err == nil {
		t.Fatal("RunBoot() error = nil, want a fatal error (repo-a's primary service crashed)")
	}

	assertFileAbsent(t, laterMarker)
}
