package a

func f() {
	go func() {}() // want "naked `go` statement forbidden"
}
