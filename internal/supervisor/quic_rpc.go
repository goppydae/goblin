package supervisor

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"sync"

	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"math"
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
			log.Printf("close RPC connection: %v", err)
		}
	}()

	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			log.Printf("AcceptStream error: %v", err)
			return
		}

		go s.handleStream(stream)
	}
}

// handleStream processes a single RPC request stream
func (s *QUICRPCServer) handleStream(stream *quic.Stream) {
	defer func() {
		if err := stream.Close(); err != nil {
			log.Printf("close RPC stream: %v", err)
		}
	}()

	// Read stream type (1 byte)
	var streamType [1]byte
	if _, err := io.ReadFull(stream, streamType[:]); err != nil {
		log.Printf("Failed to read stream type: %v", err)
		return
	}

	if goblinv1.StreamType(streamType[0]) != goblinv1.StreamType_RPC_REQUEST {
		log.Printf("Invalid stream type: %d", streamType[0])
		return
	}

	// Read request length (4 bytes)
	var lenBuf [4]byte
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		log.Printf("Failed to read request length: %v", err)
		return
	}
	reqLen := binary.BigEndian.Uint32(lenBuf[:])

	// Read request data
	reqData := make([]byte, reqLen)
	if _, err := io.ReadFull(stream, reqData); err != nil {
		log.Printf("Failed to read request data: %v", err)
		return
	}

	// Decode protobuf request
	var req goblinv1.RPCRequest
	if err := proto.Unmarshal(reqData, &req); err != nil {
		if serr := s.sendError(stream, 0, fmt.Sprintf("failed to decode request: %v", err)); serr != nil {
			log.Printf("send error response: %v", serr)
		}
		return
	}

	// Find handler
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		if serr := s.sendError(stream, req.RequestId, fmt.Sprintf("method not found: %s", req.Method)); serr != nil {
			log.Printf("send error response: %v", serr)
		}
		return
	}

	// Execute handler
	respPayload, err := handler(req.Payload)
	if err != nil {
		if serr := s.sendError(stream, req.RequestId, err.Error()); serr != nil {
			log.Printf("send error response: %v", serr)
		}
		return
	}

	// Send success response
	if err := s.sendResponse(stream, req.RequestId, respPayload, ""); err != nil {
		log.Printf("send RPC response: %v", err)
	}
}

// sendResponse sends a successful RPC response
func (s *QUICRPCServer) sendResponse(stream *quic.Stream, requestID uint32, payload []byte, errMsg string) error {
	resp := &goblinv1.RPCResponse{
		RequestId: requestID,
		Success:   errMsg == "",
		Payload:   payload,
		Error:     errMsg,
	}

	respData, err := proto.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}

	// Write stream type
	if _, err := stream.Write([]byte{byte(goblinv1.StreamType_RPC_RESPONSE)}); err != nil {
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
func (s *QUICRPCServer) sendError(stream *quic.Stream, requestID uint32, errMsg string) error {
	return s.sendResponse(stream, requestID, nil, errMsg)
}
