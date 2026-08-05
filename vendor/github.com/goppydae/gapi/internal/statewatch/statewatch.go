// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package statewatch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/anypb"

	"github.com/goppydae/gapi/core/eventbus"
	protopkg "github.com/goppydae/gapi/pkg/proto"
)

// WaitForPendingThenTerminal waits for an agent to transition to a PENDING state (if not already there/past it)
// and then to a terminal state.
func WaitForPendingThenTerminal(
	ctx context.Context,
	bus *eventbus.EventBus[*anypb.Any],
	agentID string,
	pendingTimeout time.Duration,
	finalTimeout time.Duration,
) (*protopkg.LifecycleStatus, error) {

	statusCh := make(chan *protopkg.LifecycleStatus, 8)

	// Subscribe with context-aware cleanup
	if err := bus.SubscribePrefixWithContext(ctx, "system", "", eventbus.TopicAgentLifecycleStatus, func(e eventbus.Event[*anypb.Any]) {
		var st protopkg.LifecycleStatus
		if err := eventbus.UnmarshalAnyPayload(e, &st); err != nil {
			return
		}
		if st.GetAgentId() != agentID {
			return
		}
		select {
		case statusCh <- &st:
		default:
		}
	}); err != nil {
		return nil, fmt.Errorf("subscribe to lifecycle status: %w", err)
	}

	isTerminal := func(state string) bool {
		s := strings.ToUpper(strings.TrimSpace(state))
		switch s {
		case "PENDING", "STARTING", "STOPPING", "RELOADING", "INITIALIZING", "":
			return false
		default:
			return true
		}
	}

	pendingTimer := time.NewTimer(pendingTimeout)
	defer pendingTimer.Stop()

	finalTimer := time.NewTimer(finalTimeout)
	if !finalTimer.Stop() {
		select {
		case <-finalTimer.C:
		default:
		}
	}
	activeTimer := pendingTimer.C
	seenPending := false

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case <-activeTimer:
			if !seenPending {
				return nil, fmt.Errorf("timeout waiting for PENDING")
			}
			return nil, fmt.Errorf("timeout waiting for terminal state")

		case st := <-statusCh:
			if isTerminal(st.GetState()) {
				return st, nil
			}
			if !seenPending && st.GetState() == "PENDING" {
				seenPending = true
				if !pendingTimer.Stop() {
					select {
					case <-pendingTimer.C:
					default:
					}
				}
				finalTimer.Reset(finalTimeout)
				activeTimer = finalTimer.C
				continue
			}
		}
	}
}
