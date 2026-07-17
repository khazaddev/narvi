package platform

import "time"

// Timeout literals are allowed here: this path is internal/platform, the
// one place the convention permits them (§5.4).
var Timeout = time.Second
