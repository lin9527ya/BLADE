package im

import (
	"context"
	"sync"
)

// FakeConnector is an in-memory connector for tests and for exercising the
// service without a real platform app.
type FakeConnector struct {
	Incoming chan Incoming
	Outgoing chan Outgoing
	ready    chan struct{}
	once     sync.Once
}

func NewFakeConnector() *FakeConnector {
	return &FakeConnector{
		Incoming: make(chan Incoming, 16),
		Outgoing: make(chan Outgoing, 16),
		ready:    make(chan struct{}),
	}
}

func (f *FakeConnector) Run(ctx context.Context, cb Callbacks) error {
	f.once.Do(func() { close(f.ready) })
	reportStatus(cb, "connected", nil)
	for {
		select {
		case <-ctx.Done():
			return nil
		case in := <-f.Incoming:
			if cb.OnMessage != nil {
				_ = cb.OnMessage(ctx, in)
			}
		}
	}
}

func (f *FakeConnector) Send(ctx context.Context, out Outgoing) error {
	select {
	case f.Outgoing <- out:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (f *FakeConnector) Probe(ctx context.Context) error { return nil }
func (f *FakeConnector) Close() error                    { return nil }
