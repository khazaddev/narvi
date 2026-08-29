package main

import "net/http"

// main stands for the real cmd/sandbox-agent binary's own legitimate
// client use -- must NOT be reported.
func main() {
	_, _ = http.Get("http://127.0.0.1/health")
}
