package eventbus

type Transport[T any] interface {
	PublishRemote(Event[T]) error
	Broadcast(Event[T]) error
	OnRemoteEvent(func(Event[T]))
	Close() error
}
