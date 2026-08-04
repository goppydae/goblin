// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package supervisor

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"math"

	"github.com/goppydae/goblin/core/transport"
	goblinv1 "github.com/goppydae/goblin/proto"
	"github.com/quic-go/quic-go"
	"google.golang.org/protobuf/proto"
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
	tlsConf.NextProtos = []string{transport.ALPNGoblinRPC}

	quicConfig := &quic.Config{
		MaxIdleTimeout:  60 * time.Second,
		KeepAlivePeriod: 10 * time.Second,
	}

	// A dead peer must fail dispatch fast, not hang it: unbounded
	// dials left failover instances pending forever (2b e2e).
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, addr, tlsConf, quicConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to dial %s: %w", addr, err)
	}

	return &QUICRPCClient{conn: conn}, nil
}

// roundTrip sends a payload and receives a response, handling framing and
// error decode from Task 1's typed RPCCallError. Call uses this for the
// envelope send-and-receive.
func (c *QUICRPCClient) roundTrip(method string, payload []byte) (raw []byte, err error) {
	// Create RPC request
	reqID := c.requestID.Add(1)
	rpcReq := &goblinv1.RPCRequest{
		RequestId: reqID,
		Method:    method,
		Payload:   payload,
	}

	reqData, err := proto.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal RPC request: %w", err)
	}

	// Open new stream
	stream, err := c.conn.OpenStreamSync(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}
	defer func() {
		if cerr := stream.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close stream: %w", cerr)
		}
	}()

	// Write stream type (RPC_REQUEST = 0)
	if _, err := stream.Write([]byte{byte(goblinv1.StreamType_STREAM_TYPE_RPC_REQUEST)}); err != nil {
		return nil, fmt.Errorf("failed to write stream type: %w", err)
	}

	// Write request length
	reqLen := len(reqData)
	if reqLen > math.MaxUint32 {
		return nil, fmt.Errorf("request too large to frame: %d bytes", reqLen)
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(reqLen))
	if _, err := stream.Write(lenBuf[:]); err != nil {
		return nil, fmt.Errorf("failed to write request length: %w", err)
	}

	// Write request data
	if _, err := stream.Write(reqData); err != nil {
		return nil, fmt.Errorf("failed to write request: %w", err)
	}

	// Read response stream type
	var respType [1]byte
	if _, err := io.ReadFull(stream, respType[:]); err != nil {
		return nil, fmt.Errorf("failed to read response type: %w", err)
	}

	if goblinv1.StreamType(respType[0]) != goblinv1.StreamType_STREAM_TYPE_RPC_RESPONSE {
		return nil, fmt.Errorf("invalid response stream type: %d", respType[0])
	}

	// Read response length
	if _, err := io.ReadFull(stream, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("failed to read response length: %w", err)
	}
	respLen := binary.BigEndian.Uint32(lenBuf[:])

	// Read response data
	respData := make([]byte, respLen)
	if _, err := io.ReadFull(stream, respData); err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Unmarshal RPC response
	var rpcResp goblinv1.RPCResponse
	if err := proto.Unmarshal(respData, &rpcResp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	// Check for RPC error (typed error from Task 1)
	if !rpcResp.Success {
		err = &RPCCallError{
			Code:    rpcResp.GetErrorDetail().GetCode(),
			Message: rpcResp.GetErrorDetail().GetMessage(),
		}
		return
	}

	raw = rpcResp.Payload
	return
}

// Call sends a protobuf request and decodes a protobuf response. The
// payload inside the envelope is a generated message, so buf breaking
// covers every field that crosses the wire (GOBLIN-DIV-036).
func (c *QUICRPCClient) Call(method string, req, resp proto.Message) error {
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request for %s: %w", method, err)
	}
	raw, err := c.roundTrip(method, payload)
	if err != nil {
		return err
	}
	if err := proto.Unmarshal(raw, resp); err != nil {
		return fmt.Errorf("unmarshal response for %s: %w", method, err)
	}
	return nil
}

// Close closes the QUIC connection
func (c *QUICRPCClient) Close() error {
	return c.conn.CloseWithError(0, "client closing")
}
