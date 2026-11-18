package safe

import (
	"strings"
	"testing"
	"time"
)

func TestAPanicInAGoroutineIsRecoveredAndReported(t *testing.T) {
	var got error
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer Recover("a run", func(err error) { got = err })
		var m map[string]int
		m["x"] = 1 // panics: assignment to a nil map
	}()
	<-done
	if got == nil || !strings.Contains(got.Error(), "internal error in a run") {
		t.Errorf("the panic should become an error, got %v", got)
	}
}

func TestGoKeepsTheProgramAliveAndRunsNormalWork(t *testing.T) {
	ran := make(chan bool, 2)
	Go("fine", func() { ran <- true })
	Go("broken", func() { panic("boom") })
	Go("after", func() { ran <- true })
	for i := 0; i < 2; i++ {
		select {
		case <-ran:
		case <-time.After(2 * time.Second):
			t.Fatal("work after a panic in another goroutine did not run")
		}
	}
}
