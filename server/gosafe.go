package server

import (
	"log"
	"runtime/debug"
	"sync"
)

// goSafe starts fn on a new goroutine with two guarantees a bare `go` statement
// does not give:
//
//   - a panic inside fn is contained and logged with its stack instead of
//     unwinding past the goroutine and killing the whole process. Only the HTTP
//     handler path has net/http's own recovery; every background goroutine
//     (task runners, execution reapers, side questions, log sink, LLM pool) has
//     nothing behind it, so one panic there takes the entire backend down.
//   - wg.Done runs even when fn panics, so a Wait on the group can never hang.
//     The Done defer is registered before the recover defer on purpose: defers
//     run LIFO, so recover fires first and Done still runs afterwards.
//
// name identifies the goroutine in the panic log (e.g. "engine/task-routine").
// wg may be nil when the caller does not track the goroutine.
func goSafe(name string, wg *sync.WaitGroup, fn func()) {
	go func() {
		if wg != nil {
			defer wg.Done()
		}
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[panic] %s: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}
