package supervisor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"sync"
	"time"

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
	defer conn.CloseWithError(0, "connection closed")

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
	defer stream.Close()

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
		s.sendError(stream, 0, fmt.Sprintf("failed to decode request: %v", err))
		return
	}

	// Find handler
	s.mu.RLock()
	handler, ok := s.handlers[req.Method]
	s.mu.RUnlock()

	if !ok {
		s.sendError(stream, req.RequestId, fmt.Sprintf("method not found: %s", req.Method))
		return
	}

	// Execute handler
	respPayload, err := handler(req.Payload)
	if err != nil {
		s.sendError(stream, req.RequestId, err.Error())
		return
	}

	// Send success response
	s.sendResponse(stream, req.RequestId, respPayload, "")
}

// sendResponse sends a successful RPC response
func (s *QUICRPCServer) sendResponse(stream *quic.Stream, requestID uint32, payload []byte, errMsg string) {
	resp := &goblinv1.RPCResponse{
		RequestId: requestID,
		Success:   errMsg == "",
		Payload:   payload,
		Error:     errMsg,
	}

	respData, err := proto.Marshal(resp)
	if err != nil {
		log.Printf("Failed to marshal response: %v", err)
		return
	}

	// Write stream type
	stream.Write([]byte{byte(goblinv1.StreamType_RPC_RESPONSE)})

	// Write response length
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(respData)))
	stream.Write(lenBuf[:])

	// Write response data
	stream.Write(respData)
}

// sendError sends an error RPC response
func (s *QUICRPCServer) sendError(stream *quic.Stream, requestID uint32, errMsg string) {
	s.sendResponse(stream, requestID, nil, errMsg)
}

// generateSelfSignedCert creates a self-signed certificate for development
func generateSelfSignedCert() (tls.Certificate, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	privBytes, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: privBytes})

	return tls.X509KeyPair(certPEM, keyPEM)
}
