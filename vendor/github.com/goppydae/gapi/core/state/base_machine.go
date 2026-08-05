// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package state

import (
	"fmt"
	"sync"
)

type TransitionFunc func(from, to string)

type BaseStateMachine struct {
	mu          sync.Mutex
	current     string
	transitions map[string][]string
	observers   []TransitionFunc
}

func NewBaseStateMachine(initial string, allowed map[string][]string) *BaseStateMachine {
	// Deep-copy the transition table so that mutation of the caller's map after
	// construction (or sharing one map across machines, common in tests) cannot
	// race with TransitionTo reading it under sm.mu.
	transitions := make(map[string][]string, len(allowed))
	for k, v := range allowed {
		transitions[k] = append([]string(nil), v...)
	}
	return &BaseStateMachine{
		current:     initial,
		transitions: transitions,
		observers:   make([]TransitionFunc, 0),
	}
}

func (sm *BaseStateMachine) GetState() string {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.current
}

func (sm *BaseStateMachine) TransitionTo(newState string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	allowed := sm.transitions[sm.current]
	for _, s := range allowed {
		if s == newState {
			old := sm.current
			sm.current = newState
			for _, f := range sm.observers {
				go f(old, newState)
			}
			return nil
		}
	}
	return fmt.Errorf("invalid transition: %s -> %s", sm.current, newState)
}

func (sm *BaseStateMachine) OnTransition(f TransitionFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.observers = append(sm.observers, f)
}
