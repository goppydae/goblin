package supervisor

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"math"

	"github.com/goppydae/goblin/internal/logattr"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
)

// RPCHandler processes RPC requests and returns responses
type RPCHandler func(payload []byte) ([]byte, error)

// QUICRPCServer serves RPC over QUIC
type QUICRPCServer struct {
	handlers map[string]RPCHandler
	mu       sync.RWMutex
}

// NewQUICRPCServer creates a new QUIC RPC server
func NewQUICRPCServer() *QUICRPCServer {
	return &QUICRPCServer{
		handlers: make(map[string]RPCHandler),
	}
}

// RegisterHandler registers an RPC method handler
func (s *QUICRPCServer) RegisterHandler(method string, handler RPCHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[method] = handler
}

// HandleConnection handles a single QUIC connection
func (s *QUICRPCServer) HandleConnection(conn *quic.Conn) {
	defer func() {
		if err := conn.CloseWithError(0, "connection closed"); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close rpc connection failed", logattr.Err(err))
		}
	}()

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "accept stream failed", logattr.Err(err))
			return
		}

		go s.handleStream(stream)
	}
}

// handleStream processes a single RPC request stream
func (s *QUICRPCServer) handleStream(stream *quic.Stream) {
	defer func() {
		if err := stream.Close(); err != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "close rpc stream failed", logattr.Err(err))
		}
	}()

	// Read stream type (1 byte)
	var streamType [1]byte
	if _, err := io.ReadFull(stream, streamType[:]); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to read stream type", logattr.Err(err))
		return
	}

	if goblinv1.StreamType(streamType[0]) != goblinv1.StreamType_STREAM_TYPE_RPC_REQUEST {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "invalid stream type", logattr.StreamType(int(streamType[0])))
		return
	}

	// Read request length (4 bytes)
	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to read request length", logattr.Err(err))
		return
	}
	reqLen := binary.BigEndian.Uint32(lenBuf[:])

	// Read request data
	reqData := make([]byte, reqLen)
	if _, err := io.ReadFull(stream, reqData); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "failed to read request data", logattr.Err(err))
		return
	}

	// Decode protobuf request
	var req goblinv1.RPCRequest
	if err := proto.Unmarshal(reqData, &req); err != nil {
		if serr := s.sendError(stream, 0, fmt.Errorf("failed to decode request: %w", err)); serr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "send error response failed", logattr.Err(serr))
		}
		return
	}

	// Find handler
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		if serr := s.sendError(stream, req.RequestId, fmt.Errorf("%w: %s", ErrMethodNotFound, req.Method)); serr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "send error response failed", logattr.Err(serr))
		}
		return
	}

	// Execute handler
	respPayload, handlerErr := runHandler(req.Method, handler, req.Payload)
	if handlerErr != nil {
		if serr := s.sendError(stream, req.RequestId, handlerErr); serr != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "send error response failed", logattr.Err(serr))
		}
		return
	}

	// Send success response
	if err := s.sendResponse(stream, req.RequestId, respPayload, nil); err != nil {
		slog.Default().LogAttrs(context.Background(), slog.LevelWarn, "send rpc response failed", logattr.Err(err))
	}
}

// runHandler invokes one RPC handler with a panic boundary: a panicking
// handler costs its request, never the process (GOBLIN-DIV-041). Every
// stream runs in its own goroutine, so without this recover a single
// panicking input in any handler is a remote one-request node-crash
// primitive. The panic is logged loudly with the method name and
// converted to an error that rpcErrorFor classifies INTERNAL - a
// panic is by definition an unclassified server fault.
func runHandler(method string, handler RPCHandler, payload []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Default().LogAttrs(context.Background(), slog.LevelError, "rpc handler panicked", logattr.Method(method), logattr.PanicValue(fmt.Sprintf("%v", r)))
			out = nil
			err = fmt.Errorf("handler for %s panicked: %v", method, r)
		}
	}()
	return handler(payload)
}

// sendResponse sends an RPC response. handlerErr is nil on success; when
// non-nil it is classified onto the wire as a typed RPCError and payload
// is dropped.
func (s *QUICRPCServer) sendResponse(stream *quic.Stream, requestID uint32, payload []byte, handlerErr error) error {
	resp := &goblinv1.RPCResponse{
		RequestId: requestID,
		Success:   handlerErr == nil,
		Payload:   payload,
	}
	if handlerErr != nil {
		resp.ErrorDetail = rpcErrorFor(handlerErr)
		resp.Payload = nil
	}

	respData, err := proto.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	// Write stream type
	if _, err := stream.Write([]byte{byte(goblinv1.StreamType_STREAM_TYPE_RPC_RESPONSE)}); err != nil {
		return fmt.Errorf("write stream type: %w", err)
	}

	// Write response length
	respLen := len(respData)
	if respLen > math.MaxUint32 {
		return fmt.Errorf("response too large to frame: %d bytes", respLen)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(respLen))
	if _, err := stream.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("write response length: %w", err)
	}

	// Write response data
	if _, err := stream.Write(respData); err != nil {
		return fmt.Errorf("write response data: %w", err)
	}
	return nil
}

// sendError sends an error RPC response
func (s *QUICRPCServer) sendError(stream *quic.Stream, requestID uint32, err error) error {
	return s.sendResponse(stream, requestID, nil, err)
}
