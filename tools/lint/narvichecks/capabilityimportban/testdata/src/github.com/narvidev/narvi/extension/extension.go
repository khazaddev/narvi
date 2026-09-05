// Package extension stands for the real module-facing façade, which
// legitimately imports internal/domain/license to re-export
// license.Capability as a type alias -- this doubles as this analyzer's
// own SILENCE fixture for the "extension" allowed directory.
package extension

import (
	_ "github.com/narvidev/narvi/internal/domain/license"
)

type Capability string
