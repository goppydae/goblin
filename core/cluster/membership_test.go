package cluster

import (
	"testing"
	"time"

	"github.com/hashicorp/serf/serf"
)

// TestHandleEvents_SlowHandlerDoesNotStallEventLoop verifies R25: the
// Serf event loop hands events to the dispatcher goroutine, so a slow
// (blocked) handler cannot stop the loop from draining Serf's channel.
func TestHandleEvents_SlowHandlerDoesNotStallEventLoop(t *testing.T) {
	m := &Membership{
		eventCh:   make(chan serf.Event, 4),
		handlerCh: make(chan serf.Event, 4),
	}

	block := make(chan struct{})
	handled := make(chan string, 4)
	m.SetEventHandler(func(e serf.Event) {
		ue := e.(serf.UserEvent)
		handled <- ue.Name
		<-block // first event parks the handler
	})

	go m.handleEvents()
	go m.dispatchHandler()

	m.eventCh <- serf.UserEvent{Name: "first"}
	m.eventCh <- serf.UserEvent{Name: "second"}
	m.eventCh <- serf.UserEvent{Name: "third"}

	// The handler is parked on "first"; the event loop must still have
	// drained the remaining events off eventCh into the dispatch buffer.
	deadline := time.After(2 * time.Second)
	for len(m.eventCh) > 0 {
		select {
		case <-deadline:
			t.Fatalf("event loop stalled behind a slow handler: %d events undrained", len(m.eventCh))
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Unblock and verify ordered delivery of all three.
	close(block)
	want := []string{"first", "second", "third"}
	for _, w := range want {
		select {
		case got := <-handled:
			if got != w {
				t.Fatalf("event order violated: got %q, want %q", got, w)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for event %q", w)
		}
	}

	close(m.eventCh) // shuts down both goroutines
}
