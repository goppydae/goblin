package logevent

type BusPayload struct {
	Topic   string `json:"topic"`
	Payload string `json:"payload"` // or []byte or interface{} depending on future
}
