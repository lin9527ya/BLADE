package server

import (
	"sync"
	"testing"
	"time"
)

// TestGoSafeContainsPanicAndReleasesWaitGroup pins both guarantees of goSafe: a
// panic must not escape the goroutine, and the WaitGroup must still be released
// so a Wait cannot hang forever.
func TestGoSafeContainsPanicAndReleasesWaitGroup(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	goSafe("test/panics", &wg, func() { panic("boom") })

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("wg.Wait hung: goSafe did not call Done after fn panicked")
	}
}

// TestGoSafeRunsFnAndAcceptsNilWaitGroup covers the untracked-goroutine form.
func TestGoSafeRunsFnAndAcceptsNilWaitGroup(t *testing.T) {
	ran := make(chan struct{})
	goSafe("test/nil-wg", nil, func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("fn did not run")
	}
}
