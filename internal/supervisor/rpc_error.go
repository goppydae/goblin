package supervisor

import (
	"errors"
	"fmt"

	goblinv1 "github.com/goppydae/goblin/proto"
)

// ErrInvalidRequest marks a payload the server could not decode into the
// method's request message. It is a client fault, not a server fault, so
// it maps to its own code rather than INTERNAL.
var ErrInvalidRequest = errors.New("invalid request payload")

// RPCCallError is the client-side error carrying a server RPCError.
// Callers branch on Code via errors.As; Message is for humans.
type RPCCallError struct {
	Code    goblinv1.RPCErrorCode
	Message string
}

func (e *RPCCallError) Error() string {
	return fmt.Sprintf("rpc failed (%s): %s", e.Code, e.Message)
}

// rpcErrorFor classifies a handler error for the wire. Unrecognised
// errors are INTERNAL: an unclassified failure is a server fault until
// someone proves otherwise.
func rpcErrorFor(err error) *goblinv1.RPCError {
	code := goblinv1.RPCErrorCode_RPC_ERROR_CODE_INTERNAL
	if errors.Is(err, ErrInvalidRequest) {
		code = goblinv1.RPCErrorCode_RPC_ERROR_CODE_INVALID_REQUEST
	}
	return &goblinv1.RPCError{Code: code, Message: err.Error()}
}
