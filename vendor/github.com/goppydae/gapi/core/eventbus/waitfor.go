package eventbus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/goppydae/gapi/core/clock"
)

// ErrWaitTimeout is returned when WaitForTopic's bound expires before the
// topic fires. Callers at a boot phase gate treat it as a loud failure:
// "hang forever" is not an acceptable PID 1 outcome (review R13).
var ErrWaitTimeout = errors.New("eventbus: wait for topic timed out")

// WaitForTopic blocks until an event arrives on the exact topic, the context
// is cancelled, or the timeout expires - whichever comes first. It is the
// bounded phase-gate primitive: unlike a bare subscription, it can never
// wait forever. The clock is injectable for deterministic tests; nil uses
// the real clock.
func (bus *EventBus[T]) WaitForTopic(ctx context.Context, scope, namespace, topic string, timeout time.Duration, clk clock.Clock) error {
	if clk == nil {
		clk = clock.RealClock{}
	}

	fired := make(chan struct{})
	var once sync.Once
	var wrapper Handler[T]
	wrapper = func(Event[T]) {
		once.Do(func() { close(fired) })
	}
	if err := bus.Subscribe(scope, namespace, topic, wrapper); err != nil {
		return err
	}
	defer bus.Unsubscribe(scope, namespace, topic, wrapper)

	select {
	case <-fired:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-clk.After(timeout):
		return fmt.Errorf("%w: topic %s/%s/%s did not fire within %s", ErrWaitTimeout, scope, namespace, topic, timeout)
	}
}
