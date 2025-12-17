package eventbus

import "context"

type Transport[T any] interface {
	PublishRemote(ctx context.Context, e Event[T]) error
	Broadcast(Event[T]) error
	OnRemoteEvent(func(Event[T]))
	Close() error
}
