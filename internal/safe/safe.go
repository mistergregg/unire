// Package safe keeps a panic in one place from taking the whole program down. A panic
// in a goroutine nobody recovers ends the process, and with it every chat that is
// running; here it is turned into a logged error for the one thing that failed.
package safe

import (
	"fmt"
	"log"
	"runtime/debug"
)

// Recover is for defer, as the deferred function itself: `defer safe.Recover("name", nil)`.
// It recovers a panic, logs it with its stack, and calls then (if not nil) with an error
// saying what happened, so the caller can mark what it was doing as failed.
func Recover(name string, then func(err error)) {
	if r := recover(); r != nil {
		err := fmt.Errorf("internal error in %s: %v", name, r)
		log.Printf("%v\n%s", err, debug.Stack())
		if then != nil {
			then(err)
		}
	}
}

// Go runs f in a goroutine. A panic in f is logged instead of ending the program.
func Go(name string, f func()) {
	go func() {
		defer Recover(name, nil)
		f()
	}()
}
