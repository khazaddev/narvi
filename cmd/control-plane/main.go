// Command control-plane is Narvi's control plane binary. Every line of
// wiring that used to live in this file now lives in the importable
// github.com/narvidev/narvi/controlplane package instead -- see that
// package's own doc comment for the full "why": a private module
// composing a second binary on top of Narvi needs this wiring to be a
// package it can import, not text it has to copy.
package main

import (
	"os"

	"github.com/narvidev/narvi/controlplane"
)

func main() { os.Exit(controlplane.Main(os.Args)) }
