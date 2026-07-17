package a

import "time"

var d = time.Second // want "timeout/interval literals belong in platform/timeouts.go"
