// requirecapability.go stands for the real RequireCapability middleware
// file -- the second of the two named httpapi files allowed to import the
// capability registry directly.
package httpapi

import (
	_ "github.com/narvidev/narvi/internal/app/capability"
	_ "github.com/narvidev/narvi/internal/domain/license"
)

func requireCapability() {}
