package supervisor

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
	"math"
)

// QUICRPCClient is a client for making RPC calls over QUIC
type QUICRPCClient struct {
	conn      *quic.Conn
	requestID atomic.Uint32
}

// NewQUICRPCClient creates a new QUIC RPC client
func NewQUICRPCClient(addr string, tlsConfig *tls.Config) (*QUICRPCClient, error) {
	// Clone config to avoid modifying caller's config
	tlsConf := tlsConfig.Clone()

	// Override NextProtos for Goblin RPC
	tlsConf.NextProtos = []string{"goblin-rpc"}

	quicConfig := &quic.Config{
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 10 * time.Second,
	}

	conn, err := quic.DialAddr(context.Background(), addr, tlsConf, quicConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	return &QUICRPCClient{conn: conn}, nil
}

// Call makes an RPC call and returns the response
func (c *QUICRPCClient) Call(method string, request interface{}, response interface{}) (err error) {
	// Marshal request payload
	payload, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create RPC request
	reqID := c.requestID.Add(1)
	rpcReq := &goblinv1.RPCRequest{
		RequestId: reqID,
		Method:    method,
		Payload:   payload,
	}

	reqData, err := proto.Marshal(rpcReq)
	if err != nil {
		return fmt.Errorf("failed to marshal RPC request: %w", err)
	}

	// Open new stream
	stream, err := c.conn.OpenStreamSync(context.Background())
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	// Write stream type (RPC_REQUEST = 0)
	if _, err := stream.Write([]byte{byte(goblinv1.StreamType_RPC_REQUEST)}); err != nil {
		return fmt.Errorf("failed to write stream type: %w", err)
	}

	// Write request length
	reqLen := len(reqData)
	if reqLen > math.MaxUint32 {
		return fmt.Errorf("request too large to frame: %d bytes", reqLen)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(reqLen))
	if _, err := stream.Write(lenBuf[:]); err != nil {
		return fmt.Errorf("failed to write request length: %w", err)
	}

	// Write request data
	if _, err := stream.Write(reqData); err != nil {
		return fmt.Errorf("failed to write request: %w", err)
	}

	// Read response stream type
	var respType [1]byte
	if _, err := io.ReadFull(stream, respType[:]); err != nil {
		return fmt.Errorf("failed to read response type: %w", err)
	}

	if goblinv1.StreamType(respType[0]) != goblinv1.StreamType_RPC_RESPONSE {
		return fmt.Errorf("invalid response stream type: %d", respType[0])
	}

	// Read response length
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return fmt.Errorf("failed to read response length: %w", err)
	}
	respLen := binary.BigEndian.Uint32(lenBuf[:])

	// Read response data
	respData := make([]byte, respLen)
	if _, err := io.ReadFull(stream, respData); err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	// Unmarshal RPC response
	var rpcResp goblinv1.RPCResponse
	if err := proto.Unmarshal(respData, &rpcResp); err != nil {
		return fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for RPC error
	if !rpcResp.Success {
		return fmt.Errorf("RPC error: %s", rpcResp.Error)
	}

	// Unmarshal response payload
	if response != nil && len(rpcResp.Payload) > 0 {
		if err := json.Unmarshal(rpcResp.Payload, response); err != nil {
			return fmt.Errorf("failed to unmarshal response payload: %w", err)
		}
	}

	return nil
}

// Close closes the QUIC connection
func (c *QUICRPCClient) Close() error {
	return c.conn.CloseWithError(0, "client closing")
}
