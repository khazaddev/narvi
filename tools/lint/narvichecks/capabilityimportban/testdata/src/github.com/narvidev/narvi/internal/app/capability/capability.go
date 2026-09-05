// Package capability stands for the real internal/app/capability, which
// legitimately imports internal/domain/license for Grant/Capability --
// this doubles as this analyzer's own SILENCE fixture for the
// "internal/app/capability" allowed directory.
package capability

import (
	_ "github.com/narvidev/narvi/internal/domain/license"
)

type Registry struct{}
