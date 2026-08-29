package main

import "os/exec"

// main stands for the real cmd/sandbox-agent binary's own legitimate
// subprocess use (running the agent runtime) -- must NOT be reported.
func main() {
	_ = exec.Command("true").Run()
}
