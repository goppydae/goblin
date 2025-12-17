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
	return &BaseStateMachine{
		current:     initial,
		transitions: allowed,
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
	return fmt.Errorf("invalid transition: %s → %s", sm.current, newState)
}

func (sm *BaseStateMachine) OnTransition(f TransitionFunc) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.observers = append(sm.observers, f)
}
