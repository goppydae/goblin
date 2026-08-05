// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package agentmgr

import (
	"fmt"

	"github.com/goppydae/gapi/core/lifecycle"
)

func (am *AgentManager) StartAll() error {
	order, err := TopologicalSort(am.All())
	if err != nil {
		return err
	}
	for _, id := range order {
		a := am.Get(id)
		if a == nil {
			continue
		}
		if err := a.Controller().Apply(lifecycle.ActionStart); err != nil {
			return fmt.Errorf("start %s: %w", id, err)
		}
	}
	return nil
}

func (am *AgentManager) StopAll() error {
	order, err := TopologicalSort(am.All())
	if err != nil {
		return err
	}
	for i := len(order) - 1; i >= 0; i-- {
		id := order[i]
		a := am.Get(id)
		if a == nil {
			continue
		}
		if err := a.Controller().Apply(lifecycle.ActionStop); err != nil {
			return fmt.Errorf("stop %s: %w", id, err)
		}
	}
	return nil
}
