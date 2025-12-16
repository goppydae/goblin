package transport

import (
	"github.com/goppydae/gapi/internal/eventbus"

	"google.golang.org/protobuf/types/known/anypb"
)

// All transports are now strictly protobuf-based.
type Transport = eventbus.Transport[*anypb.Any]
