package server

import (
	"sync"

	"github.com/lin9527ya/blade/db"
)

// Broadcaster is a per-task in-process pub/sub for live activity events. The
// engine publishes each appended activity at its single emit point; SSE handlers
// subscribe per task. Storage (activity table) and the live stream come from the
// same Publish call, so they never diverge.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[string]map[chan db.Activity]struct{} // task id -> set of subscriber channels
	// overflow marks a subscriber whose buffer filled. The event itself is lost by
	// design (liveness must never block the engine), but the stream handler sees
	// this and force-closes the connection so EventSource reconnects with its
	// Last-Event-ID and replays the gap from the DB.
	overflow map[string]map[chan db.Activity]bool
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		subs:     map[string]map[chan db.Activity]struct{}{},
		overflow: map[string]map[chan db.Activity]bool{},
	}
}

// Subscribe returns a buffered channel of activities for a task plus an
// unsubscribe func the caller must invoke (defer) to release it.
func (b *Broadcaster) Subscribe(task string) (chan db.Activity, func()) {
	ch := make(chan db.Activity, 256)
	b.mu.Lock()
	if b.subs[task] == nil {
		b.subs[task] = map[chan db.Activity]struct{}{}
	}
	b.subs[task][ch] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			if m := b.subs[task]; m != nil {
				delete(m, ch)
				if len(m) == 0 {
					delete(b.subs, task)
				}
			}
			if m := b.overflow[task]; m != nil {
				delete(m, ch)
				if len(m) == 0 {
					delete(b.overflow, task)
				}
			}
			b.mu.Unlock()
			// Do NOT close(ch): Publish snapshots channels under the lock and sends
			// after unlocking. A concurrent send/close is a panic; dropping the
			// channel from the maps is enough, and the handler exits on ctx.Done.
		})
	}
}

// Publish fans an activity out to all subscribers of a task. Non-blocking: if a
// subscriber's buffer is full the event is dropped — the client reconnects with
// its last seq cursor and catches up the gap from the DB, so liveness never
// stalls the engine.
func (b *Broadcaster) Publish(task string, a db.Activity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[task] {
		select {
		case ch <- a:
		default:
			if b.overflow[task] == nil {
				b.overflow[task] = map[chan db.Activity]bool{}
			}
			b.overflow[task][ch] = true
		}
	}
}

// Overflowed reports whether this subscriber lost at least one event. The stream
// handler polls it and closes the connection, forcing a DB-backed resync.
func (b *Broadcaster) Overflowed(task string, ch chan db.Activity) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflow[task][ch]
}
