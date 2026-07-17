package c

import "testing"

// Proves the rule applies inside _test.go files too — §11 grants no test
// carve-out for naked goroutines (unlike the timeout-literal rule).
func TestNoExemption(t *testing.T) {
	go func() {}() // want "naked `go` statement forbidden"
}
