package app

import (
	"fmt"
	"sync"
)

// State is the server lifecycle state.
type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDegraded State = "degraded"
	StateDraining State = "draining"
	StateStopped  State = "stopped"
)

// Event drives the lifecycle machine.
type Event string

const (
	EventStarted     Event = "started"      // startup checks passed, listener open, Immich reachable
	EventStartedDown Event = "started_down" // startup checks passed, Immich unreachable
	EventProbeOK     Event = "probe_ok"
	EventProbeFailed Event = "probe_failed"
	EventSignal      Event = "signal"
	EventDrained     Event = "drained"
)

// transition is the only place lifecycle states change.
func transition(s State, e Event) (State, error) {
	switch s {
	case StateStarting:
		switch e {
		case EventStarted:
			return StateReady, nil
		case EventStartedDown:
			return StateDegraded, nil
		case EventSignal:
			return StateDraining, nil
		}
	case StateReady, StateDegraded:
		switch e {
		case EventProbeOK:
			return StateReady, nil
		case EventProbeFailed:
			return StateDegraded, nil
		case EventSignal:
			return StateDraining, nil
		}
	case StateDraining:
		if e == EventDrained {
			return StateStopped, nil
		}
	}
	return s, fmt.Errorf("lifecycle: illegal event %s in state %s", e, s)
}

// lifecycle holds the current state.
type lifecycle struct {
	mu    sync.Mutex
	state State
}

func (l *lifecycle) get() State {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state
}

// fire applies e; illegal events (a probe result after SIGTERM) are ignored.
func (l *lifecycle) fire(e Event) State {
	l.mu.Lock()
	defer l.mu.Unlock()
	if next, err := transition(l.state, e); err == nil {
		l.state = next
	}
	return l.state
}
