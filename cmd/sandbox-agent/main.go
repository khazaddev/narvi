// Command sandbox-agent is the static binary shipped into sandbox images
// (§1). Step 13 gave it its first real behavior: boot-mode/hook-policy
// decisions (internal/domain/sandboxboot) plus native process supervision
// (internal/sandboxagent/supervisor) -- process groups, killpg-style
// signaling, reaping, and bounded graceful-then-forceful shutdown. Step 14
// extends its boot dispatch to also supervise a per-repo .narvi/
// services.yml multi-service manifest (internal/sandboxagent/services,
// §14.2) when one is present, falling back to the original setup.sh/
// start.sh hook contract otherwise -- both orchestrated by
// internal/sandboxagent/boot.RunBoot. It logs a boot fingerprint first
// (§5.3), runs the boot sequence for whatever repo list names, then blocks
// until told to shut down.
//
// Step 15 adds two things: (1) when Config.SessionConfig is present (the
// NARVI_SESSION_CONFIG env var was set), run() clones every repo it names
// (internal/sandboxagent/gitclone.CloneAll) and writes the generated
// AGENTS.md manifest BEFORE handing the successfully-cloned subset to
// boot.RunBoot as its []boot.RepoInfo -- when SessionConfig is nil (the
// common dev/test case), repos stays nil exactly as before this Step; (2)
// a SEPARATE "credential-helper" subcommand (main's own dispatch, mirroring
// cmd/control-plane/main.go's own subcommand pattern) that implements
// git's credential-helper protocol end to end (internal/sandboxagent/
// credentials) -- this is the exact command gitclone configures every
// `git clone` to invoke via `-c credential.helper=!'<this binary>'
// credential-helper` (§5.2).
//
// Step 16 wires the real sandbox WS bridge (internal/sandboxagent/wsbridge)
// in place of the slog-only boot_progress reporter Step 14 left as an
// explicit placeholder: when Config.SessionConfig is present, run() builds
// a *wsbridge.Bridge and drives it via bridge.Run(ctx) alongside the
// existing OS-signal-driven shutdown -- whichever finishes first (an OS
// signal cancels ctx, or the control plane sends a "shutdown" command, or
// the handshake returns a fatal 401/403/404/410 status) converges on the
// SAME StopAll-based graceful shutdown Step 13 built, except a fatal
// connect status propagates as run()'s own error instead. As of Step 16,
// prompt/stop/push/snapshot/git_sync_complete were all wired to a log-only
// stub handler.
//
// Step 17 lands the real OpenCode adapter (internal/adapters/outbound/
// opencode) and its process-spawning sibling
// (internal/sandboxagent/opencodeproc): when Config.SessionConfig is
// present, run() spawns `opencode serve` (via opencodeproc.Spawn, which
// itself reuses the SAME Supervisor already tracking every other
// supervised process -- StopAll's own existing graceful shutdown reaps it
// too, no new cleanup code needed) BEFORE the WS bridge starts accepting
// commands -- a "prompt" command can arrive as soon as the bridge connects,
// concurrently with the boot/clone sequence (Step 16's own design), so the
// adapter must already exist by then. commandHandler.HandlePrompt now
// launches the actual turn (adapter.StartTurn) on its own goroutine (via
// this Step's own new commandHandler.group field, an errgroup.Group --
// never a bare `go` statement, §11) rather than running it inline: wsbridge
// dispatches HandlePrompt synchronously from its own readLoop, and a turn
// can run for minutes -- if it blocked readLoop directly, a "stop" command
// arriving while a turn is in flight would never be read, let alone acted
// on, defeating Stop's entire purpose. That goroutine deliberately uses
// run()'s own long-lived, OS-signal-driven ctx (captured in commandHandler
// at construction), NOT the shorter-lived, per-WS-connection ctx wsbridge's
// dispatch hands to HandlePrompt/HandleStop -- a turn that outlives a mere
// WS reconnect must not be aborted just because the connection blipped
// (the wsbridge ack protocol already handles redelivering the turn's own
// events across a reconnect independently of whether the turn itself is
// still running). git_sync_complete remains the EXACT log-only stub Step
// 16 shipped -- Step 29 (gitstate in-sandbox) is that command's own job,
// per docs/IMPLEMENTATION_PLAN.md's own Phase 2 row assignment; leave it
// exactly as it is.
//
// Step 21 ("e2e happy path") gives push its own real behavior:
// commandHandler.HandlePush now runs a real `git push` (via the SAME
// Supervisor every other supervised process already uses, configured with
// the SAME per-invocation credential-helper convention CloneAll already
// established for `git clone`), then reports a real sandboxws.
// PushComplete (with the resulting HEAD sha per repo) or PushError over
// the WS bridge.
//
// Step 22 ("snapshots & restore") gives snapshot its own real behavior:
// commandHandler.HandleSnapshot now calls the control plane's new
// snapshot-mint endpoint (internal/sandboxagent/snapshotclient, design
// decision 2) to obtain a real, sandbox-confirmed snapshotId, then reports
// a real CRITICAL sandboxws.SnapshotReady over the WS bridge (design
// decision 4) -- see HandleSnapshot's own doc comment for the full round
// trip and its one honest, documented failure-reporting gap (no NACK
// event exists on the wire for a failed snapshot attempt).
//
// Step 28 ("turn recovery", §3.3) fixes commandHandler.HandlePrompt's own
// conversation-id reporting to genuinely happen "at turn start... never
// lazily": it now passes a ports.ConversationIDReporter callback into
// StartTurn (adapter.go invokes it immediately once resolveSession
// resolves a real id, well before the rest of a turn's own, possibly-
// minutes-long execution), calling h.bridge.SetConversationID from inside
// that callback -- replacing the old Step-17 wiring, which only ever read
// StartTurn's own RETURN value, meaning the first report of a real
// conversation id used to happen only after a turn had basically already
// ended. wsbridge.Bridge.SetConversationID itself (internal/sandboxagent/
// wsbridge) now also triggers an immediate, out-of-band heartbeat send the
// first time it observes a genuinely new, non-nil id, rather than waiting
// for its own next regular tick.
//
// Step 29 ("gitstate in-sandbox", §3.4) gives runBootSequence real
// boot-mode-aware dispatch: BootModeBuild/BootModeFresh keep calling
// gitclone.CloneAll (a fresh clone into an empty directory) unchanged;
// BootModeRepoImage/BootModeSnapshotRestore instead call the new
// gitclone.SyncAll against the workspace's own ALREADY-EXISTING repo
// (baked into the image or restored from a snapshot) -- the real
// "stash-if-dirty -> checkout session branch (create from base if absent)
// -> stash pop" sequence, driven through internal/domain/gitstate's own
// Transition table. Each real phase fires a best-effort sandboxws.GitSync
// event over the WS bridge (onGitSync, run()); HandleGitSyncComplete's own
// log-only behavior is now documented as deliberate, final behavior, not a
// stub. A BootModeBuild boot additionally runs gitclone.CleanForImageBuild
// once RunBoot itself succeeds, discarding untracked/modified residue
// setup.sh may have left behind, so the tree is guaranteed clean by the
// time a provider is free to snapshot it (§3.4: "Image builds must
// snapshot a clean tree"). CloneAll itself also gained sparse-checkout
// support (§14.1): a session's own Environment.PathScope, now threaded
// through SessionConfig's own new optional pathScope field, restricts a
// freshly-cloned repo's working tree to exactly those patterns.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	"github.com/khazaddev/narvi/contracts/gen/go/sandboxws"
	"github.com/khazaddev/narvi/internal/adapters/outbound/opencode"
	"github.com/khazaddev/narvi/internal/app/ports"
	"github.com/khazaddev/narvi/internal/domain/reposource"
	"github.com/khazaddev/narvi/internal/domain/sandboxboot"
	"github.com/khazaddev/narvi/internal/platform"
	"github.com/khazaddev/narvi/internal/sandboxagent/boot"
	"github.com/khazaddev/narvi/internal/sandboxagent/credentials"
	"github.com/khazaddev/narvi/internal/sandboxagent/gitclone"
	"github.com/khazaddev/narvi/internal/sandboxagent/opencodeproc"
	"github.com/khazaddev/narvi/internal/sandboxagent/services"
	"github.com/khazaddev/narvi/internal/sandboxagent/snapshotclient"
	"github.com/khazaddev/narvi/internal/sandboxagent/supervisor"
	"github.com/khazaddev/narvi/internal/sandboxagent/wsbridge"
)

func main() {
	// A bare-bones dispatch, not a flag-parsing library, mirroring
	// cmd/control-plane/main.go's own subcommand pattern: exactly one
	// alternate subcommand exists today ("credential-helper", the process
	// git itself invokes per gitclone's own `-c credential.helper=...`
	// configuration) -- everything else falls through to the normal boot
	// sequence.
	if len(os.Args) >= 2 && os.Args[1] == "credential-helper" {
		if err := runCredentialHelper(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runCredentialHelper implements the "credential-helper <get|store|erase>"
// subcommand git itself invokes (via gitclone's own `-c
// credential.helper=!'<this binary>' credential-helper` configuration on
// every clone). This is a SEPARATE process git spawns, inheriting
// sandbox-agent's own environment (supervisor.Spec.Env is nil/inherit in
// gitclone's Spawn call) -- so re-reading the same NARVI_* env vars here
// via boot.Load() is correct and sufficient, not a duplication of that
// config-loading logic.
func runCredentialHelper(args []string) error {
	if len(args) != 1 {
		return errors.New("sandbox-agent: credential-helper: usage: sandbox-agent credential-helper <get|store|erase>")
	}

	op := args[0]
	if op != "get" && op != "store" && op != "erase" {
		return fmt.Errorf("sandbox-agent: credential-helper: unknown op %q, want get/store/erase", op)
	}

	cfg, err := boot.Load()
	if err != nil {
		return fmt.Errorf("sandbox-agent: credential-helper: load config: %w", err)
	}

	if op == "store" {
		return credentials.RunStore(os.Stdin)
	}

	cache := &credentials.Cache{Dir: cfg.CredentialCacheDir}

	if op == "erase" {
		return credentials.RunErase(os.Stdin, cache)
	}

	// op == "get" from here on -- the only op that actually needs a live
	// SessionConfig (ControlPlaneWsUrl/SessionId/SandboxToken to mint a
	// fresh credential from CP). store/erase need neither, so gating them
	// on it too would (in some future scenario where erase is invoked
	// without one) block purging a bad cache entry -- working against the
	// very goal RunErase exists for.
	if cfg.SessionConfig == nil {
		return errors.New(
			"sandbox-agent: credential-helper: get: NARVI_SESSION_CONFIG is unset -- nothing to fetch credentials for",
		)
	}

	timeouts := platform.DefaultTimeouts()
	client, err := credentials.NewCPClient(cfg.SessionConfig.ControlPlaneWsUrl, timeouts.CredentialFetchTimeout)
	if err != nil {
		return fmt.Errorf("sandbox-agent: credential-helper: build CP client: %w", err)
	}

	return credentials.RunGet(
		context.Background(), os.Stdin, os.Stdout, cache, client,
		cfg.SessionConfig.SessionId, cfg.SessionConfig.SandboxToken, cfg.SessionConfig.Gen, timeouts.CredentialExpiryBuffer,
	)
}

// commandHandler is sandbox-agent's own wsbridge.CommandHandler
// implementation. Step 16 shipped it as an empty, log-only struct for all
// 5 commands; Step 17 gives HandlePrompt/HandleStop their real behavior
// (push/snapshot/git_sync_complete are untouched, still each their own
// later Step's job, confirmed against docs/IMPLEMENTATION_PLAN.md rather
// than guessed).
//
// A *commandHandler (pointer receiver, unlike Step 16's value-receiver
// empty struct) because adapter/bridge are populated in TWO phases: run()
// constructs a *commandHandler with adapter already set, passes it to
// wsbridge.New as the CommandHandler interface value, THEN sets .bridge on
// that SAME pointer once the Bridge itself exists -- Bridge and
// commandHandler each need a reference to the other (HandlePrompt forwards
// events onto the bridge; the bridge dispatches commands to the handler),
// so one of the two references must be filled in after construction.
type commandHandler struct {
	// adapter is nil exactly when cfg.SessionConfig is nil (see run()) --
	// HandlePrompt/HandleStop below treat that as "no live session, no
	// OpenCode to talk to" and log a warning instead of dispatching,
	// mirroring this Step's own "no real session, no work" precedent from
	// every prior sandbox-agent Step. In practice a *commandHandler is
	// only ever constructed at all within that same cfg.SessionConfig !=
	// nil branch (see run()), so this nil check is defense-in-depth, not
	// a path this binary's own current wiring can actually reach.
	adapter *opencode.Adapter
	bridge  *wsbridge.Bridge

	// runCtx is run()'s own long-lived, OS-signal-driven context --
	// deliberately NOT the shorter-lived, per-WS-connection ctx wsbridge's
	// own dispatch hands to HandlePrompt/HandleStop (see the package doc
	// comment's own Step 17 paragraph for why: a turn must survive a mere
	// WS reconnect, not be aborted by one).
	runCtx context.Context

	// cfg/timeouts/sup are Step 21's ("e2e happy path") own additions,
	// needed by HandlePush: cfg.WorkspaceDir/SessionConfig.Repos locate
	// each repo and its original clone URL (to determine which host to
	// mint a git credential for); timeouts bounds the push/rev-parse
	// subprocesses; sup is the SAME Supervisor run() already constructs
	// and uses for every other supervised process (hooks, services,
	// opencode) -- HandlePush reuses it rather than spawning bare
	// exec.Command calls, matching internal/sandboxagent/gitclone's own
	// "never a bare exec.Command" precedent exactly.
	cfg      boot.Config
	timeouts platform.Timeouts
	sup      *supervisor.Supervisor

	// group launches each HandlePrompt's own StartTurn call on its own
	// goroutine (via errgroup.Group.Go, never a bare `go` statement, §11)
	// so wsbridge's own readLoop -- which calls HandlePrompt synchronously
	// -- is never blocked for an entire turn's duration; a "stop" command
	// arriving mid-turn must still reach HandleStop promptly. Deliberately
	// a zero-value errgroup.Group, never Wait()'d on except once, at
	// shutdown (run()'s own final drain) -- the SAME "one launched unit's
	// own failure/completion must never cancel a sibling's independent
	// work" reasoning as internal/sandboxagent/supervisor.Supervisor's own
	// analogous field.
	group errgroup.Group
}

func (h *commandHandler) HandlePrompt(_ context.Context, cmd sandboxws.Prompt) {
	if h.adapter == nil {
		slog.Warn("sandbox-agent: received prompt but no OpenCode adapter is configured (no live session)",
			"messageId", cmd.MessageId)
		return
	}

	h.group.Go(func() error {
		sink := func(event ports.AgentEvent) {
			if event.Critical {
				if err := h.bridge.SendCritical(h.runCtx, event.Payload, event.AckID); err != nil {
					slog.Warn("sandbox-agent: send critical agent event over WS bridge failed",
						"messageId", cmd.MessageId, "ackId", event.AckID, "error", err)
				}
				return
			}
			if err := h.bridge.SendBestEffort(h.runCtx, event.Payload); err != nil {
				slog.Warn("sandbox-agent: send best-effort agent event over WS bridge failed",
					"messageId", cmd.MessageId, "error", err)
			}
		}

		// Step 28 ("turn recovery"), §3.3 "at turn start... never lazily":
		// report the conversation id to the bridge THE INSTANT StartTurn
		// itself resolves it (adapter.go's own resolveSession, called
		// long before the rest of a turn's own, possibly-minutes-long
		// execution) via this callback -- moved out of its old Step-17
		// position of reading StartTurn's own RETURN value only after the
		// whole call completed (which meant the FIRST report of a real
		// conversation id used to only ever happen after a turn had
		// basically already ended, directly contradicting §3.3). That old
		// post-return read is gone entirely: onConversationID's callback
		// already covers every path that ever resolves a real id (see
		// ports.ConversationIDReporter's own doc comment), so a second
		// read of StartTurn's own return value here would be redundant,
		// dead code, not a genuinely different case.
		onConversationID := func(id string) {
			h.bridge.SetConversationID(&id)
		}

		if _, err := h.adapter.StartTurn(h.runCtx, cmd, sink, onConversationID); err != nil {
			slog.Warn("sandbox-agent: StartTurn returned an error", "messageId", cmd.MessageId, "error", err)
		}
		// Never propagate a per-turn error into this shared group -- one
		// turn's own failure must never affect a sibling launched call.
		return nil
	})
}

func (h *commandHandler) HandleStop(_ context.Context, cmd sandboxws.Stop) {
	if h.adapter == nil {
		slog.Warn("sandbox-agent: received stop but no OpenCode adapter is configured (no live session)",
			"messageId", cmd.MessageId)
		return
	}
	if err := h.adapter.Stop(h.runCtx, cmd); err != nil {
		slog.Warn("sandbox-agent: Stop failed", "messageId", cmd.MessageId, "error", err)
	}
}

// HandlePush implements the real `push` command (Step 21, "e2e happy
// path", design decision 7): for each repo named in cmd.Repos, runs a
// plain `git push <remote> <branch>` (this Step's own happy-path scope --
// no pre-existing dirty-tree reconciliation; internal/domain/gitstate's
// stash/checkout/pop machinery is explicitly Step 29's own job, not
// touched here), configured with the SAME per-invocation, never-
// persisted `-c credential.helper=!'<this binary>' credential-helper`
// convention internal/sandboxagent/gitclone.CloneAll already uses for
// cloning (gitclone.CredHelperGitArg, exported this Step specifically for
// this second caller) -- git itself invokes the credential-helper
// subcommand (internal/sandboxagent/credentials) exactly as it already
// does for clone, so no new credential-fetching code path is needed here
// at all, and §5.2's "never long-lived in sandbox" holds by construction
// (nothing is ever written to a persistent git-credential store).
//
// On the FIRST repo that fails, sends a single sandboxws.PushError (this
// wire type carries one error string, not a per-repo breakdown) and stops
// -- matching Push's own schema, which has no partial-success shape to
// report into.
func (h *commandHandler) HandlePush(_ context.Context, cmd sandboxws.Push) {
	if h.cfg.SessionConfig == nil {
		slog.Warn("sandbox-agent: received push but no live session is configured", "messageId", cmd.MessageId)
		return
	}

	completed := make([]sandboxws.PushCompleteReposElem, 0, len(cmd.Repos))
	for _, repoSpec := range cmd.Repos {
		sha, err := h.pushOneRepo(repoSpec)
		if err != nil {
			slog.Warn("sandbox-agent: push failed", "messageId", cmd.MessageId, "repo", repoSpec.Name, "error", err)
			h.sendPushError(cmd, err)
			return
		}
		completed = append(completed, sandboxws.PushCompleteReposElem{
			Name: repoSpec.Name, Branch: repoSpec.Branch, Sha: sha,
		})
	}

	h.sendPushComplete(cmd, completed)
}

// pushOneRepo runs `git push` for exactly one repo, then reads back the
// resulting HEAD sha, both via h.sup (never a bare exec.Command).
//
// Every one of repoSpec.Name/Branch/Remote is session-controlled
// (sandboxws.Push.Repos[], relayed from the control plane) and is
// validated via internal/domain/reposource BEFORE this repo's target
// directory is even computed (filepath.Join) or any Args are built for
// h.sup.Spawn -- the exact same argument-injection/path-traversal
// reasoning internal/sandboxagent/gitclone.CloneAll's own validateRepoSpec
// closes for clone, applied here for push's own two call-site-specific
// fields (remote, in addition to name/branch). See reposource's own
// package doc comment for the full reasoning.
func (h *commandHandler) pushOneRepo(repoSpec sandboxws.PushReposElem) (string, error) {
	if err := reposource.ValidateRepoName(repoSpec.Name); err != nil {
		return "", fmt.Errorf("invalid repo name: %w", err)
	}
	if err := reposource.ValidateBranch(repoSpec.Branch); err != nil {
		return "", fmt.Errorf("invalid repo branch: %w", err)
	}

	remote := "origin"
	if repoSpec.Remote != nil && *repoSpec.Remote != "" {
		remote = *repoSpec.Remote
	}
	if err := reposource.ValidateRemoteName(remote); err != nil {
		return "", fmt.Errorf("invalid remote: %w", err)
	}

	dir := filepath.Join(h.cfg.WorkspaceDir, repoSpec.Name)

	credHelperArg, err := gitclone.CredHelperGitArg()
	if err != nil {
		return "", fmt.Errorf("determine credential helper: %w", err)
	}

	// RepoCloneTimeout is reused here rather than a distinct field:
	// `git push` and `git clone` are both single, network-bound git
	// transport operations against the SAME remote, and a push can carry
	// a comparably large object set (a fresh branch's full diff) -- the
	// same generous, "large repo" budget CloneAll's own calls already use
	// (see RepoCloneTimeout's own doc comment) fits push equally well,
	// matching headSHA's own precedent just below of reusing an existing
	// Timeouts field for a materially identical class of operation rather
	// than inventing a near-duplicate one.
	pushCtx, cancel := context.WithTimeout(h.runCtx, h.timeouts.RepoCloneTimeout)
	defer cancel()

	var stderr bytes.Buffer
	proc, err := h.sup.Spawn(supervisor.Spec{
		Path: "git",
		// "--" ends option parsing for everything after it (verified
		// directly against real `git push` behavior, not assumed) --
		// defense in depth alongside the validation above: even an
		// already-validated remote/branch should never be positionally
		// ambiguous to git's own argument parser.
		Args:   []string{"-C", dir, "-c", "credential.helper=" + credHelperArg, "push", "--", remote, repoSpec.Branch},
		Stderr: &stderr,
		// Env is DELIBERATELY left at its zero value (nil, "inherit this
		// process's own environment") -- a reviewed choice, not an
		// oversight. git's own credential.helper mechanism re-execs THIS
		// SAME sandbox-agent binary as `<binary> credential-helper get`,
		// as git's OWN child process, inheriting whatever env git itself
		// received here -- i.e. exactly what this Spec.Env carries,
		// nothing more. runCredentialHelper's own boot.Load() call reads
		// NARVI_SESSION_CONFIG via os.Getenv and fails outright if it is
		// absent (see its own "nothing to fetch credentials for" error),
		// so stripping it here would BREAK git authentication for every
		// push -- a real functional regression, not a hardening win. A
		// hand-built allowlist would also risk silently omitting something
		// the real `git` binary or its transport (http/ssh) legitimately
		// needs (PATH, HOME, an ssh-agent socket, ...) that isn't yet
		// enumerated anywhere in this codebase. See gitclone.cloneOne's own
		// identical comment for the clone-side counterpart of this exact
		// reasoning.
	})
	if err != nil {
		return "", fmt.Errorf("spawn git push for %s: %w", repoSpec.Name, err)
	}

	result, waitErr := proc.Wait(pushCtx)
	if waitErr != nil {
		_ = proc.Stop(h.runCtx, h.timeouts.ProcessStopGracePeriod)
		return "", fmt.Errorf("git push %s: did not complete within %s: %w", repoSpec.Name, h.timeouts.RepoCloneTimeout, waitErr)
	}
	if result.Err != nil {
		return "", fmt.Errorf("git push %s: %w", repoSpec.Name, result.Err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("git push %s: exited %d: %s", repoSpec.Name, result.ExitCode, strings.TrimSpace(stderr.String()))
	}

	sha, err := h.headSHA(dir)
	if err != nil {
		return "", fmt.Errorf("determine head sha for %s: %w", repoSpec.Name, err)
	}
	return sha, nil
}

// headSHA runs `git rev-parse HEAD` in dir and returns its trimmed
// stdout -- a very minor, sub-second local git-plumbing call (matching
// platform.Timeouts.RepoSHADiscoveryTimeout's own existing "boot
// fingerprint" precedent exactly, reused rather than duplicated for a
// materially identical class of operation), still run via h.sup (never a
// bare exec.Command) using supervisor.Spec's own new Stdout field.
func (h *commandHandler) headSHA(dir string) (string, error) {
	ctx, cancel := context.WithTimeout(h.runCtx, h.timeouts.RepoSHADiscoveryTimeout)
	defer cancel()

	var stdout bytes.Buffer
	proc, err := h.sup.Spawn(supervisor.Spec{
		Path:   "git",
		Args:   []string{"-C", dir, "rev-parse", "HEAD"},
		Stdout: &stdout,
		// Unlike pushOneRepo's own git push Spawn call just above (which
		// deliberately keeps full env inheritance -- see its own comment),
		// this is a purely local, no-network, no-credential-helper
		// plumbing command: no `-c credential.helper=...` flag is ever set
		// for it, so it has no structural need for NARVI_SESSION_CONFIG
		// (the sandbox's own plaintext bearer token) at all. Tightened for
		// defense in depth.
		Env: supervisor.EnvWithout(boot.SessionConfigEnvVar),
	})
	if err != nil {
		return "", fmt.Errorf("spawn git rev-parse HEAD: %w", err)
	}

	result, waitErr := proc.Wait(ctx)
	if waitErr != nil {
		_ = proc.Stop(h.runCtx, h.timeouts.ProcessStopGracePeriod)
		return "", fmt.Errorf("did not complete within %s: %w", h.timeouts.RepoSHADiscoveryTimeout, waitErr)
	}
	if result.Err != nil {
		return "", result.Err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("exited %d", result.ExitCode)
	}
	return strings.TrimSpace(stdout.String()), nil
}

// sendPushComplete sends a real, CRITICAL sandboxws.PushComplete event --
// MessageId/AckId are freshly minted here (this event ORIGINATES from
// sandbox-agent; it does not echo cmd's own MessageId), matching
// internal/adapters/outbound/opencode/translate.go's own
// translateExecutionComplete precedent for every agent-originated
// critical event exactly ("Deterministic ackId = '{type}:{messageId}'"
// where messageId is THIS event's own, per contracts/sandbox-ws/v1's own
// doc comments).
func (h *commandHandler) sendPushComplete(cmd sandboxws.Push, repos []sandboxws.PushCompleteReposElem) {
	messageID := uuid.NewString()
	msg := sandboxws.PushComplete{
		Type:      "push_complete",
		MessageId: messageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		AckId:     "push_complete:" + messageID,
		Repos:     repos,
	}
	if err := h.bridge.SendCritical(h.runCtx, msg, msg.AckId); err != nil {
		slog.Warn("sandbox-agent: send push_complete over WS bridge failed",
			"messageId", cmd.MessageId, "ackId", msg.AckId, "error", err)
	}
}

// sendPushError mirrors sendPushComplete for the failure path -- a single
// error string, per PushError's own schema (no per-repo breakdown).
func (h *commandHandler) sendPushError(cmd sandboxws.Push, pushErr error) {
	messageID := uuid.NewString()
	msg := sandboxws.PushError{
		Type:      "push_error",
		MessageId: messageID,
		SessionId: cmd.SessionId,
		Gen:       cmd.Gen,
		AckId:     "push_error:" + messageID,
		Error:     pushErr.Error(),
	}
	if err := h.bridge.SendCritical(h.runCtx, msg, msg.AckId); err != nil {
		slog.Warn("sandbox-agent: send push_error over WS bridge failed",
			"messageId", cmd.MessageId, "ackId", msg.AckId, "error", err)
	}
}

// HandleSnapshot implements the real `snapshot` command (Step 22,
// "snapshots & restore", design decision 4): calls the control plane's own
// new snapshot-mint endpoint (internal/sandboxagent/snapshotclient,
// design decision 2 -- the real TakeSnapshot network call can only be
// made by the control plane itself, which alone holds the provider's own
// credentials) to obtain a real, sandbox-confirmed snapshotId, then
// reports it back as a CRITICAL "snapshot_ready" event over the WS bridge
// (mirroring sendPushComplete's own exact call shape: a fresh MessageId/
// AckId minted here, this event originates from sandbox-agent, it does
// not echo cmd's own MessageId as ITS OWN MessageId) -- except CommandMessageId,
// which IS deliberately set to cmd.MessageId verbatim: the control plane's
// own message-id-correlation fix (sessionactor's triggerSnapshotBestEffort/
// handleSnapshotReadyEvent) needs this snapshot_ready echoed back to the
// exact Snapshot command it answers, to tell two attempts on the same live
// sandbox apart (gen alone can't: a snapshot cycle happens within the same
// gen) -- mirroring how a request-id is normally echoed in a response.
//
// On any failure obtaining the id (no live session configured, an invalid
// ControlPlaneWsUrl, the CP request itself failing, or CP returning a
// non-2xx/malformed response): log and return without sending anything --
// design decision 2's own honest, documented limitation: no NACK-shaped
// event exists on the wire to tell the control plane "never mind". HONEST
// GAP (documented, not fixed by this Step -- see sessionactor's own
// triggerSnapshotBestEffort doc comment for the matching statement of the
// same fact from the control-plane side): the control plane's own
// Snapshotting state is only ever recovered by a real snapshot_ready (this
// function never sending one is exactly the case that can't recover it) or
// by revertSnapshotBestEffort's own SendCommand-failure detection -- a
// mint failure THIS function hits AFTER a Snapshot command was already
// successfully delivered is invisible to that detection, so the sandbox is
// left stuck Snapshotting with no watchdog covering that state until a
// later reconnect/restart cycle, or -- more honestly -- until a future
// Step adds a real NACK/timeout mechanism this Step's own plan-row text
// does not ask for (explicitly out of THIS Step's own scope: no new
// dedicated snapshot-timeout timer, no broader NACK mechanism).
func (h *commandHandler) HandleSnapshot(_ context.Context, cmd sandboxws.Snapshot) {
	if h.cfg.SessionConfig == nil {
		slog.Warn("sandbox-agent: received snapshot but no live session is configured", "messageId", cmd.MessageId)
		return
	}

	client, err := snapshotclient.New(h.cfg.SessionConfig.ControlPlaneWsUrl, h.timeouts.SnapshotMintTimeout)
	if err != nil {
		slog.Warn("sandbox-agent: snapshot: build CP client failed", "messageId", cmd.MessageId, "error", err)
		return
	}

	snapshotID, err := client.Mint(h.runCtx, h.cfg.SessionConfig.SessionId, h.cfg.SessionConfig.SandboxToken)
	if err != nil {
		slog.Warn("sandbox-agent: snapshot: mint request to control plane failed", "messageId", cmd.MessageId, "error", err)
		return
	}

	messageID := uuid.NewString()
	commandMessageID := cmd.MessageId
	msg := sandboxws.SnapshotReady{
		Type:             "snapshot_ready",
		MessageId:        messageID,
		SessionId:        cmd.SessionId,
		Gen:              cmd.Gen,
		AckId:            "snapshot_ready:" + messageID,
		SnapshotId:       snapshotID,
		CommandMessageId: &commandMessageID,
	}
	if err := h.bridge.SendCritical(h.runCtx, msg, msg.AckId); err != nil {
		slog.Warn("sandbox-agent: send snapshot_ready over WS bridge failed",
			"messageId", cmd.MessageId, "ackId", msg.AckId, "error", err)
	}
}

// HandleGitSyncComplete observes the control plane's own best-effort
// acknowledgment of a git_sync event (§3.4, Step 29 "gitstate in-sandbox").
// This sandbox's own git-sync reconciliation (internal/sandboxagent/
// gitclone.SyncAll, driven from runBootSequence below) is entirely
// sandbox-local -- git doesn't need CP permission to run stash/checkout/
// pop -- and git_sync itself carries no ackId (it is a best-effort event,
// not one of the 6 critical types per events.schema.json), so the sandbox
// runs its own reconciliation sequence at its own pace and never blocks or
// waits for this reply before proceeding to the next phase. Log-only is
// this handler's deliberate, final behavior, not a stub: there is nothing
// further for it to do.
func (*commandHandler) HandleGitSyncComplete(_ context.Context, cmd sandboxws.GitSyncComplete) {
	slog.Info("sandbox-agent: received git_sync_complete (observational only; sandbox-side git-sync does not gate on it)",
		"messageId", cmd.MessageId)
}

// run mirrors cmd/control-plane/main.go's serve() shape: a thin main()
// dispatches to this testable, error-returning function.
func run() error {
	// boot.Load() is the earliest possible failure -- before any logging
	// setup even -- exactly like control-plane's own platform.Load()
	// failure path.
	cfg, err := boot.Load()
	if err != nil {
		return fmt.Errorf("sandbox-agent: load config: %w", err)
	}

	logger := platform.NewLogger(os.Stdout, cfg.LogLevel)
	slog.SetDefault(logger)

	// sandbox-agent has no env-driven timeout-override mechanism (neither
	// does control-plane's own platform.Load(), which also always uses
	// DefaultTimeouts() verbatim), so reuse the shared defaults directly.
	timeouts := platform.DefaultTimeouts()

	// §5.3: "sandbox-agent logs a boot fingerprint first" -- this MUST be
	// the very first line this binary emits; nothing above this point
	// logs anything. openCodeVersion is necessarily "" here (§7's own
	// discovery requires the OpenCode server to already be running, which
	// hasn't happened yet) -- see the supplementary fingerprint log below,
	// once it has.
	fingerprint := boot.CollectFingerprint(cfg, timeouts.RepoSHADiscoveryTimeout, "")
	slog.Info("sandbox-agent: boot fingerprint",
		"agent_version", fingerprint.AgentVersion,
		"image_digest", fingerprint.ImageDigest,
		"boot_mode", string(fingerprint.BootMode),
		"repo_shas", fingerprint.RepoSHAs,
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	sup := supervisor.New()

	// agentRuntime is nil exactly when cfg.SessionConfig is nil (the
	// common dev/test case with no real session) -- there is nothing to
	// prompt at all, matching this Step's own "no real session, no work"
	// precedent from every prior sandbox-agent Step. Spawned BEFORE the WS
	// bridge starts accepting commands (below): a "prompt" command can
	// arrive as soon as the bridge connects, concurrently with the boot/
	// clone sequence (Step 16's own design), so the adapter must already
	// exist by then -- see this file's own package doc comment for the
	// full reasoning.
	var agentRuntime *opencode.Adapter
	if cfg.SessionConfig != nil {
		// opencodeproc.Spawn's own Dir is cfg.WorkspaceDir -- normally
		// created by gitclone.CloneAll (runBootSequence, below), which
		// hasn't run yet at this point. Ensure it exists NOW instead of
		// letting the spawn fail on a nonexistent chdir target; idempotent
		// (os.MkdirAll no-ops if it already exists) and harmless for
		// CloneAll's own later MkdirAll call.
		if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
			return fmt.Errorf("sandbox-agent: create workspace dir: %w", err)
		}

		result, spawnErr := opencodeproc.Spawn(ctx, sup, cfg.WorkspaceDir,
			timeouts.OpenCodeReadinessTimeout, timeouts.OpenCodeReadinessPollInterval)
		if spawnErr != nil {
			// Best-effort cleanup of whatever sup may already be tracking
			// -- opencodeproc.Spawn's own internal Supervisor.Spawn call
			// registers the process before its readiness check even
			// starts, so a timed-out or crashed process is still known to
			// sup here -- before failing fast, the same "boot.Load() is
			// the earliest possible failure" shape as this function's own
			// very first error path.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), timeouts.SupervisorShutdownTimeout)
			_ = sup.StopAll(shutdownCtx, timeouts.ProcessStopGracePeriod)
			cancel()
			return fmt.Errorf("sandbox-agent: spawn opencode: %w", spawnErr)
		}

		agentRuntime = opencode.New(result.BaseURL, timeouts.SSEInactivityTimeout,
			timeouts.OpenCodeSSEReconnectInterval, timeouts.OpenCodeRequestTimeout)
		defer agentRuntime.Close()

		// §7: "Pin the OpenCode version in the image; record it in the
		// boot fingerprint" -- the FIRST fingerprint log (above)
		// necessarily reported this as empty; now that OpenCode has
		// actually been spawned, log a SECOND, supplementary line -- the
		// SAME "log first with what's known, then a supplementary line
		// once more is known" pattern Step 15 already established for
		// repo_shas (see runBootSequence's own post-clone fingerprint
		// log).
		postSpawnFingerprint := boot.CollectFingerprint(cfg, timeouts.RepoSHADiscoveryTimeout, result.Version)
		slog.Info("sandbox-agent: boot fingerprint (post-opencode-spawn)",
			"opencode_version", postSpawnFingerprint.OpenCodeVersion)
	}

	// bridge/handler are nil exactly when cfg.SessionConfig is nil --
	// everything below that branches on "bridge != nil" preserves today's
	// original no-bridge behavior unchanged in that case. sandboxID is
	// boot.Load()'s own resolveSandboxID value -- see Config.SandboxID's
	// own doc comment for where it really comes from now.
	//
	// handler is constructed with adapter already set, passed to
	// wsbridge.New as the CommandHandler interface value, THEN gets
	// .bridge set on that SAME pointer once Bridge exists -- see
	// commandHandler's own doc comment for why this two-phase
	// construction is necessary (Bridge and commandHandler each need a
	// reference to the other).
	var bridge *wsbridge.Bridge
	var handler *commandHandler
	if cfg.SessionConfig != nil {
		handler = &commandHandler{adapter: agentRuntime, runCtx: ctx, cfg: cfg, timeouts: timeouts, sup: sup}
		bridge = wsbridge.New(*cfg.SessionConfig, cfg.SandboxID, handler,
			timeouts.SandboxWSDialTimeout, timeouts.SandboxWSHeartbeatInterval,
			timeouts.SandboxWSReconnectMinBackoff, timeouts.SandboxWSReconnectMaxBackoff)
		handler.bridge = bridge
	}

	// reportBootProgress forwards each §6.1 boot_progress event over the
	// real WS bridge when one exists (a live session) -- AND always logs a
	// service-level failure locally too, regardless of whether a bridge
	// exists: an earlier version of this Step only logged event.Err in the
	// nil-bridge fallback branch, silently dropping the diagnostic reason
	// for a real service-boot failure whenever a live bridge was present
	// (the wire boot_progress event itself has no error field to carry it
	// either, so the local log is the ONLY place this information survives
	// at all on that path).
	reportBootProgress := func(event services.BootProgressEvent) {
		if bridge != nil {
			if sendErr := bridge.SendBootProgress(ctx, event); sendErr != nil {
				slog.Warn("sandbox-agent: send boot_progress over WS bridge failed",
					"service", event.ServiceName, "phase", string(event.Phase), "error", sendErr)
			}
		}
		if event.Phase == services.PhaseFailed {
			slog.Info("sandbox-agent: boot_progress",
				"service", event.ServiceName, "phase", string(event.Phase), "error", event.Err)
			return
		}
		if bridge == nil {
			slog.Info("sandbox-agent: boot_progress", "service", event.ServiceName, "phase", string(event.Phase))
		}
	}

	// Start the WS bridge (or, when there's no live session, the equivalent
	// ctx-wait goroutine) BEFORE cloning/booting -- not after. An earlier
	// version of this Step only launched bridge.Run once runBootSequence
	// below had already returned, which meant NO WS connection existed for
	// the entire boot window: nothing reportBootProgress sent during
	// cloning/hooks/services would reach the control plane until boot was
	// already fully done, silently defeating §3.2's "boot-progress reports
	// re-arm the connecting deadline" rule and resilience scenario §9.3 #3
	// (a slow boot must not cause a false kill). Bridge.Run's own outbound
	// buffer already handles "SendBootProgress called before the connection
	// has finished dialing" gracefully (buffer now, flush once connected),
	// so starting it concurrently with the boot sequence costs nothing and
	// fixes a real bug. A single errgroup.Group either way (no naked `go`
	// statement, §11): the nil-bridge branch's own goroutine does exactly
	// what a direct `<-ctx.Done()` would, just launched through the group
	// so both cases converge identically below.
	var group errgroup.Group
	if bridge != nil {
		group.Go(func() error {
			return bridge.Run(ctx)
		})
	} else {
		group.Go(func() error {
			<-ctx.Done()
			return nil
		})
	}

	// onGitSync translates each internal/sandboxagent/gitclone.SyncAll phase
	// (§3.4, Step 29 "gitstate in-sandbox") into an outbound sandboxws.
	// GitSync event, mirroring reportBootProgress's own "forward over the
	// bridge when one exists, always log locally too" shape immediately
	// above. git_sync is a best-effort event with no ackId (events.
	// schema.json's own GitSync def), sent via the SAME Bridge.
	// SendBestEffort every other non-critical event already uses
	// (SendBootProgress just above, HandlePrompt's own sink closure) --
	// SyncAll itself never blocks or waits on this call's own outcome
	// before proceeding to its next phase. Only ever invoked from within
	// runBootSequence's own cfg.SessionConfig != nil branch (below), so
	// cfg.SessionConfig is always non-nil here in practice; the nil guards
	// below mirror reportBootProgress's own defensive style even though,
	// by that exact same construction, neither bridge nor cfg.SessionConfig
	// is ever nil whenever this closure is actually invoked.
	onGitSync := func(repoName, status, branch string) {
		slog.Info("sandbox-agent: git_sync", "repo", repoName, "status", status, "branch", branch)
		if bridge == nil || cfg.SessionConfig == nil {
			return
		}
		msg := sandboxws.GitSync{
			Type:      "git_sync",
			MessageId: uuid.NewString(),
			SessionId: cfg.SessionConfig.SessionId,
			Gen:       cfg.SessionConfig.Gen,
			Status:    sandboxws.GitSyncStatus(status),
			Branch:    branch,
			Repo:      repoName,
		}
		if sendErr := bridge.SendBestEffort(ctx, msg); sendErr != nil {
			slog.Warn("sandbox-agent: send git_sync over WS bridge failed",
				"repo", repoName, "status", status, "error", sendErr)
		}
	}

	bootErr := runBootSequence(ctx, sup, cfg, timeouts, reportBootProgress, onGitSync)
	if bootErr != nil {
		// A boot failure needs the same graceful convergence a fatal WS
		// status or an OS signal gets -- cancel ctx (stop is a genuine
		// context.CancelFunc, signal.NotifyContext's own doc comment;
		// calling it more than once, e.g. again via the deferred call
		// above, is safe) so the bridge/ctx-wait goroutine above actually
		// unwinds instead of running forever waiting for a reason to stop
		// that a boot failure alone would never give it.
		stop()
	} else {
		if bridge != nil {
			bridge.MarkBootComplete()
		}
		slog.Info("sandbox-agent: boot sequence complete")
	}

	runErr := group.Wait()

	// Drain every turn goroutine commandHandler.HandlePrompt launched
	// (handler.group) before proceeding to the supervised-process shutdown
	// below -- by this point ctx is already canceled (whatever triggered
	// the convergence above), so any in-flight StartTurn call is already
	// unwinding per its own documented ctx-cancellation contract; this is
	// a bounded wait, not an open-ended one. A nil handler (cfg.
	// SessionConfig was nil) has nothing to drain.
	if handler != nil {
		_ = handler.group.Wait()
	}

	// Always attempt a bounded graceful shutdown of every supervised
	// process, regardless of why the above finished -- a fatal WS status or
	// a boot failure must not skip cleanup and orphan whatever hooks/
	// services already started (Setpgid'd process groups have no
	// Pdeathsig; nothing else reaps them if sandbox-agent exits without
	// running this).
	slog.Info("sandbox-agent: shutting down", "grace_period", timeouts.SupervisorShutdownTimeout.String())

	// Deliberately a fresh background context, not ctx: by this point ctx
	// is already canceled (that's exactly what triggers this shutdown),
	// and a canceled context would make StopAll fail immediately instead
	// of actually bounding the drain -- same reasoning as control-plane's
	// own shutdownOTel deferred call.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeouts.SupervisorShutdownTimeout)
	defer cancel()
	stopErr := sup.StopAll(shutdownCtx, timeouts.ProcessStopGracePeriod)

	// A fatal handshake status (401/403/404/410, §6.1: "no retry") is NOT a
	// normal shutdown trigger -- it must propagate as run()'s own error
	// (main() then os.Exit(1)s) even though StopAll above already ran.
	var fatalErr *wsbridge.FatalConnectError
	if errors.As(runErr, &fatalErr) {
		return fmt.Errorf("sandbox-agent: WS bridge: %w", runErr)
	}

	// Every other outcome -- wsbridge.ErrShutdownRequested (a CP-issued
	// "shutdown" command), nil, or ctx.Err() (an OS signal via
	// signal.NotifyContext, or the boot-failure-triggered stop() above) --
	// is a normal convergence path, not logged as unexpected.
	if runErr != nil && !errors.Is(runErr, wsbridge.ErrShutdownRequested) && !errors.Is(runErr, context.Canceled) {
		// Not expected per Bridge.Run's own documented contract (only nil,
		// ctx.Err(), ErrShutdownRequested, or *FatalConnectError -- already
		// handled above -- are ever returned), but logged defensively
		// rather than silently ignored if that contract is ever violated.
		slog.Warn("sandbox-agent: WS bridge Run returned an unexpected error", "error", runErr)
	}

	if bootErr != nil {
		return fmt.Errorf("sandbox-agent: boot: %w", bootErr)
	}
	return stopErr
}

// runBootSequence prepares every repo cfg.SessionConfig names (in order),
// writes the generated AGENTS.md manifest (§6.4), then runs boot.RunBoot
// against the successfully-prepared subset. repos/preparation is skipped
// entirely when cfg.SessionConfig is nil (the common dev/test case) --
// boot.RunBoot's own documented, correct no-op on an empty repo list
// handles that unchanged from Step 14.
//
// Step 29 ("gitstate in-sandbox", §3.4) splits "prepare every repo" on
// cfg.BootMode, the exact, principled dispatch point internal/domain/
// sandboxboot.BootMode already names: BootModeBuild/BootModeFresh mean "no
// prior image/snapshot to build on" -- the workspace does NOT yet exist on
// disk -- so this is the existing gitclone.CloneAll path, UNCHANGED, a
// real `git clone` into an empty directory. BootModeRepoImage/
// BootModeSnapshotRestore mean the workspace's own real git repo ALREADY
// EXISTS on disk (baked into the image at build time, or restored from a
// snapshot), so this instead calls the NEW gitclone.SyncAll (§3.4:
// "stash-if-dirty -> checkout session branch -> stash pop") -- CloneAll is
// never called on this path at all; cloning again into a non-empty
// directory would conflict with what is already there.
func runBootSequence(
	ctx context.Context,
	sup *supervisor.Supervisor,
	cfg boot.Config,
	timeouts platform.Timeouts,
	reportBootProgress services.ProgressReporter,
	onGitSync gitclone.OnGitSync,
) error {
	var repos []boot.RepoInfo
	if cfg.SessionConfig != nil {
		var manifestInput []gitclone.CloneResult

		switch cfg.BootMode {
		case sandboxboot.BootModeRepoImage, sandboxboot.BootModeSnapshotRestore:
			results, syncErr := gitclone.SyncAll(ctx, sup, cfg.WorkspaceDir, cfg.SessionConfig.Repos,
				cfg.SessionConfig.SessionId, timeouts.GitSyncStepTimeout, timeouts.ProcessStopGracePeriod, onGitSync)
			if syncErr != nil {
				return fmt.Errorf("sync repos: %w", syncErr)
			}
			manifestInput = make([]gitclone.CloneResult, len(results))
			for i, result := range results {
				manifestInput[i] = result.ToCloneResult()
				if result.Err != nil {
					continue
				}
				repos = append(repos, boot.RepoInfo{Name: result.Repo.Name, Primary: result.Primary})
			}
		default:
			// BootModeBuild/BootModeFresh (and any future mode this switch
			// does not yet know about -- boot.Load()'s own ParseBootMode has
			// already rejected anything outside the four §6.4 values by the
			// time cfg reaches here, so falling through to the existing,
			// pre-Step-29 behavior is the correct, conservative default).
			//
			// pathScope (§14.1) is wired into ONLY this fresh-clone path --
			// a repo_image/snapshot_restore boot (the other switch case,
			// above) reuses an already-cloned, already-sparse-configured
			// workspace: git preserves a working tree's own sparse-checkout
			// config across subsequent checkouts within that same repo, so
			// re-invoking it there would be redundant, not wrong, but
			// unnecessary complexity this Step's own brief explicitly
			// declines to add.
			var pathScope []string
			if cfg.SessionConfig.PathScope != nil {
				pathScope = []string(*cfg.SessionConfig.PathScope)
			}
			results, cloneErr := gitclone.CloneAll(ctx, sup, cfg.WorkspaceDir, cfg.SessionConfig.Repos, pathScope,
				timeouts.RepoCloneTimeout, timeouts.ProcessStopGracePeriod)
			if cloneErr != nil {
				return fmt.Errorf("clone repos: %w", cloneErr)
			}
			manifestInput = results
			for _, result := range results {
				if result.Err != nil {
					continue
				}
				repos = append(repos, boot.RepoInfo{Name: result.Repo.Name, Primary: result.Primary})
			}
		}

		if err := gitclone.WriteAgentsManifest(cfg.WorkspaceDir, manifestInput); err != nil {
			return fmt.Errorf("write AGENTS.md: %w", err)
		}

		// The FIRST boot-fingerprint line (in run(), above this function)
		// necessarily reported repo_shas as empty -- §5.3 requires it
		// logged first, and nothing was cloned/synced yet at that point. Now
		// that this repo preparation has happened (via either branch above),
		// re-collect and log an updated fingerprint so repo_shas actually
		// carries the information §5.3 asks for on this exact path; nothing
		// about the original "logs first" line is changed or replaced, this
		// is a second, supplementary log line. openCodeVersion is passed as
		// "" here -- this log line's own purpose is repo_shas specifically
		// (run()'s own separate "post-opencode-spawn" supplementary line
		// already covers that field, logged before runBootSequence is ever
		// called).
		postCloneFingerprint := boot.CollectFingerprint(cfg, timeouts.RepoSHADiscoveryTimeout, "")
		slog.Info("sandbox-agent: boot fingerprint (post-clone)",
			"repo_shas", postCloneFingerprint.RepoSHAs,
		)
	}

	if err := boot.RunBoot(ctx, sup, cfg.WorkspaceDir, repos, cfg.BootMode, reportBootProgress,
		timeouts.HookTimeout, timeouts.ProcessStopGracePeriod,
		timeouts.ServiceReadinessTimeout, timeouts.ServiceReadinessPollInterval); err != nil {
		return fmt.Errorf("boot: %w", err)
	}

	// §3.4 ("Image builds must snapshot a clean tree") / Step 29's own
	// Part E: ONLY for a BootModeBuild boot, and ONLY once RunBoot itself
	// has already returned successfully -- a failed setup.sh in build mode
	// is already fatal per BootModeBuild's own existing primary-fatal
	// semantics (sandboxboot.EvaluateHook), so this point is never reached
	// on that failure path at all; there is nothing to gate here beyond
	// "did RunBoot succeed". This runs BEFORE this function returns --
	// i.e. before whatever the existing boot-sequence-complete signal is
	// (run()'s own "boot sequence complete" log line and bridge.
	// MarkBootComplete call, immediately after runBootSequence returns) --
	// so that by the time the provider is free to snapshot, the tree is
	// guaranteed clean. repos is empty (a correct no-op) whenever
	// cfg.SessionConfig was nil, exactly like every other repos-driven step
	// above.
	if cfg.BootMode == sandboxboot.BootModeBuild {
		repoNames := make([]string, len(repos))
		for i, r := range repos {
			repoNames[i] = r.Name
		}
		if err := gitclone.CleanForImageBuild(ctx, sup, cfg.WorkspaceDir, repoNames,
			timeouts.GitSyncStepTimeout, timeouts.ProcessStopGracePeriod); err != nil {
			return fmt.Errorf("clean workspace before snapshot: %w", err)
		}
	}

	return nil
}
