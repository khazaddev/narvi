// Temporary diagnostic program -- to be removed once the real cause of
// the opencode-serve health-check timeout on CI is found. Bisects two
// candidate variables between opencodeproc.Spawn (fails on GitHub
// Actions) and a plain background shell job (succeeds): the freePort()
// bind-close-reuse dance, and exec.Cmd's Setpgid:true process-group setup.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer func() { _ = l.Close() }()
	port, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return port.Port, nil
}

func healthCheck(baseURL string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

func waitHealthy(baseURL string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if healthCheck(baseURL) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// scenario A: fixed port, Setpgid:true, stdout/stderr nil (-> /dev/null) --
// isolates whether Setpgid+devnull alone (no freePort dance) reproduces it.
func scenarioA() {
	fmt.Println("=== scenario A: fixed port 5001, Setpgid:true, stdout/stderr nil ===")
	cmd := exec.Command("opencode", "serve", "--port", "5001", "--hostname", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Println("start error:", err)
		return
	}
	defer func() { _ = cmd.Process.Kill() }()
	ok := waitHealthy("http://127.0.0.1:5001", 20*time.Second)
	fmt.Println("scenario A healthy within 20s:", ok)
}

// scenario B: freePort's own bind-close-reuse dance, plain exec.Command
// with NO Setpgid, stdout/stderr nil -- isolates whether the freePort race
// alone (no Setpgid) reproduces it.
func scenarioB() {
	fmt.Println("=== scenario B: freePort() dance, no Setpgid, stdout/stderr nil ===")
	port, err := freePort()
	if err != nil {
		fmt.Println("freePort error:", err)
		return
	}
	fmt.Println("freePort chose:", port)
	cmd := exec.Command("opencode", "serve", "--port", fmt.Sprintf("%d", port), "--hostname", "127.0.0.1")
	if err := cmd.Start(); err != nil {
		fmt.Println("start error:", err)
		return
	}
	defer func() { _ = cmd.Process.Kill() }()
	ok := waitHealthy(fmt.Sprintf("http://127.0.0.1:%d", port), 20*time.Second)
	fmt.Println("scenario B healthy within 20s:", ok)
}

// scenario C: freePort's own dance AND Setpgid:true together (the full,
// exact combination opencodeproc.Spawn/supervisor.Spawn actually use).
func scenarioC() {
	fmt.Println("=== scenario C: freePort() dance + Setpgid:true, stdout/stderr nil (the real combination) ===")
	port, err := freePort()
	if err != nil {
		fmt.Println("freePort error:", err)
		return
	}
	fmt.Println("freePort chose:", port)
	cmd := exec.Command("opencode", "serve", "--port", fmt.Sprintf("%d", port), "--hostname", "127.0.0.1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		fmt.Println("start error:", err)
		return
	}
	defer func() { _ = cmd.Process.Kill() }()
	ok := waitHealthy(fmt.Sprintf("http://127.0.0.1:%d", port), 20*time.Second)
	fmt.Println("scenario C healthy within 20s:", ok)
}

func main() {
	scenarioA()
	scenarioB()
	scenarioC()
	os.Exit(0)
}
