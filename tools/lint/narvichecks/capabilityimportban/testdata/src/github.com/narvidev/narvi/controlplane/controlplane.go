// Package controlplane stands for the real composition root -- the one
// package allowed to import all three capability-decision packages at
// once: it parses the licence key, builds the registry, and wires
// RequireCapability onto every module route.
package controlplane

import (
	_ "github.com/narvidev/narvi/extension"
	_ "github.com/narvidev/narvi/internal/app/capability"
	_ "github.com/narvidev/narvi/internal/domain/license"
)

func build() {}
