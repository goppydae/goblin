// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package cli

import (
	"context"
	"fmt"
	"net/rpc"
	"strings"

	"github.com/goppydae/gapi/core/tui"
	"github.com/hashicorp/serf/serf"
)

// ClusterController implements tui.AgentControl for Goblin cluster using Serf RPC
type ClusterController struct {
	apiAddr string
}

func NewClusterController(addr string) *ClusterController {
	return &ClusterController{apiAddr: addr}
}

func (c *ClusterController) FetchStatus(ctx context.Context) (_ []tui.AgentStatus, err error) {
	// Connect to Serf RPC
	host := c.apiAddr
	if idx := strings.LastIndex(c.apiAddr, ":"); idx > 0 {
		host = c.apiAddr[:idx]
	}
	serfRPCAddr := fmt.Sprintf("%s:7373", host)

	client, err := rpc.Dial("tcp", serfRPCAddr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Serf RPC at %s: %w", serfRPCAddr, err)
	}
	defer func() {
		if cerr := client.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("close rpc client: %w", cerr)
		}
	}()

	// Get cluster members
	var members []serf.Member
	if err := client.Call("Serf.Members", struct{}{}, &members); err != nil {
		return nil, fmt.Errorf("failed to get members: %w", err)
	}

	// Convert to AgentStatus format
	var statuses []tui.AgentStatus
	for _, m := range members {
		status := tui.AgentStatus{
			ID:    m.Name,
			State: m.Status.String(),
			Type:  "cluster-node",
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func (c *ClusterController) Lifecycle(ctx context.Context, id, action string) (bool, error) {
	return false, fmt.Errorf("lifecycle operations are not supported in cluster mode - use `goblinctl agent` for node-local agent management")
}

func (c *ClusterController) GetLogs(ctx context.Context, id string) (<-chan string, error) {
	ch := make(chan string)
	close(ch)
	return ch, fmt.Errorf("distributed logs not implemented yet")
}
