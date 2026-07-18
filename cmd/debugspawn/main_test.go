package main

import "testing"

// TestScenarios wraps the exact same 3 scenarios from main() as a real Go
// test, isolating `go test`'s own harness as the last untested variable
// (already ruled out via plain `go run` and `go run -race`: broken
// binary/install, cross-package concurrency, empty tempdir working
// directory, freePort(), Setpgid, and -race).
func TestScenarios(t *testing.T) {
	scenarioA()
	scenarioB()
	scenarioC()
}
