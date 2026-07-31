package supervisor

import (
	"errors"
	"fmt"

	"github.com/goppydae/goblin/core/consensus"
	goblinv1 "github.com/goppydae/goblin/proto"
)

// ErrInvalidRequest marks a payload the server could not decode into the
// method's request message. It is a client fault, not a server fault, so
// it maps to its own code rather than INTERNAL.
var ErrInvalidRequest = errors.New("invalid request payload")

// ErrMethodNotFound marks a dispatch against a method the server has no
// handler for. It is a routing miss, not a server fault, so it maps to
// its own code rather than INTERNAL.
var ErrMethodNotFound = errors.New("method not found")

// RPCCallError is the client-side error carrying a server RPCError.
// Callers branch on Code via errors.As; Message is for humans.
type RPCCallError struct {
	Code    goblinv1.RPCErrorCode
	Message string
}

func (e *RPCCallError) Error() string {
	return fmt.Sprintf("rpc failed (%s): %s", e.Code, e.Message)
}

// IsPermissionDenied reports whether err is a server refusal classified
// PERMISSION_DENIED. It is the client-side half of the contract stated
// in rpc.proto - callers branch on the code, message text is for humans
// and is never matched on - and exists so no caller has to reach for
// strings.Contains to tell a deliberate refusal from a server fault.
func IsPermissionDenied(err error) bool {
	var call *RPCCallError
	return errors.As(err, &call) &&
		call.Code == goblinv1.RPCErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED
}

// rpcErrorFor classifies a handler error for the wire. Unrecognised
// errors are INTERNAL: an unclassified failure is a server fault until
// someone proves otherwise.
func rpcErrorFor(err error) *goblinv1.RPCError {
	code := goblinv1.RPCErrorCode_RPC_ERROR_CODE_INTERNAL
	switch {
	case errors.Is(err, ErrInvalidRequest):
		code = goblinv1.RPCErrorCode_RPC_ERROR_CODE_INVALID_REQUEST
	case errors.Is(err, ErrMethodNotFound):
		code = goblinv1.RPCErrorCode_RPC_ERROR_CODE_NOT_FOUND
	case errors.Is(err, consensus.ErrOperatorRegistryEmpty):
		// The fail-closed gate (GOBLIN-DIV-015 piece 1) refused on
		// purpose, so it must not go out as INTERNAL. INTERNAL means
		// "server fault", and both audiences act on that: a client that
		// retries internal errors would hammer a deliberate refusal, and
		// an operator triaging the fault would go hunting a server bug
		// that is not there. PERMISSION_DENIED says what happened and is
		// the code callers branch on instead of the message text.
		code = goblinv1.RPCErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED
	}
	return &goblinv1.RPCError{Code: code, Message: err.Error()}
}
