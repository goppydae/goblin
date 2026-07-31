package supervisor

import (
	"errors"
	"fmt"
	"testing"

	"github.com/goppydae/goblin/core/consensus"
	goblinv1 "github.com/goppydae/goblin/proto"
)

func TestRPCCallError_CarriesCode(t *testing.T) {
	tests := []struct {
		name string
		code goblinv1.RPCErrorCode
	}{
		{"invalid request", goblinv1.RPCErrorCode_RPC_ERROR_CODE_INVALID_REQUEST},
		{"permission denied", goblinv1.RPCErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED},
		{"internal", goblinv1.RPCErrorCode_RPC_ERROR_CODE_INTERNAL},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var err error = &RPCCallError{Code: tc.code, Message: "detail"}

			var got *RPCCallError
			if !errors.As(err, &got) {
				t.Fatalf("errors.As failed for %v", tc.code)
			}
			if got.Code != tc.code {
				t.Errorf("Code = %v, want %v", got.Code, tc.code)
			}
		})
	}
}

func TestRPCErrorFor_DefaultsToInternal(t *testing.T) {
	e := rpcErrorFor(errors.New("something broke"))
	if e.GetCode() != goblinv1.RPCErrorCode_RPC_ERROR_CODE_INTERNAL {
		t.Errorf("code = %v, want INTERNAL", e.GetCode())
	}
	if e.GetMessage() != "something broke" {
		t.Errorf("message = %q, want %q", e.GetMessage(), "something broke")
	}
}

func TestRPCErrorFor_MapsInvalidRequest(t *testing.T) {
	e := rpcErrorFor(ErrInvalidRequest)
	if e.GetCode() != goblinv1.RPCErrorCode_RPC_ERROR_CODE_INVALID_REQUEST {
		t.Errorf("code = %v, want INVALID_REQUEST", e.GetCode())
	}
}

func TestRPCErrorFor_MapsMethodNotFound(t *testing.T) {
	e := rpcErrorFor(fmt.Errorf("%w: NodeRPC.NoSuchMethod", ErrMethodNotFound))
	if e.GetCode() != goblinv1.RPCErrorCode_RPC_ERROR_CODE_NOT_FOUND {
		t.Errorf("code = %v, want NOT_FOUND", e.GetCode())
	}
}

// TestRPCErrorFor_MapsOperatorRegistryEmpty pins the fail-closed gate's
// refusal to PERMISSION_DENIED. It is the only classified refusal on the
// wire, and callers branch on the code rather than the message: without
// this mapping the refusal arrives as INTERNAL and every caller has to
// string-match to tell a deliberate refusal from a server fault.
func TestRPCErrorFor_MapsOperatorRegistryEmpty(t *testing.T) {
	e := rpcErrorFor(fmt.Errorf("%w: agent.register refused", consensus.ErrOperatorRegistryEmpty))
	if e.GetCode() != goblinv1.RPCErrorCode_RPC_ERROR_CODE_PERMISSION_DENIED {
		t.Errorf("code = %v, want PERMISSION_DENIED", e.GetCode())
	}
}
