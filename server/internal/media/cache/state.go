package cache

import (
	"fmt"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/media/sanitize"
)

// Phase is the state of a media job, one per cache key.
type Phase string

const (
	Absent  Phase = "absent"
	Queued  Phase = "queued"
	Running Phase = "running"
	// Committed: the file is on disk and verified by this process.
	Committed Phase = "committed"
	// Recovered: the file was found on disk at startup and is verified
	// again before it is first served.
	Recovered Phase = "recovered"
	Failed    Phase = "failed"
)

// Priority is the queue a job waits in and the slot class it runs in.
type Priority int

const (
	Prewarm Priority = iota
	Visitor
)

// State is the full job state.
type State struct {
	Phase    Phase
	Priority Priority            // queued, running
	Step     int                 // running
	Reason   sanitize.FailReason // failed
	Until    time.Time           // failed
}

type eventKind string

const (
	evVisitor    eventKind = "visitor"    // a visitor wants the file
	evPrewarm    eventKind = "prewarm"    // prewarm wants the file
	evSlot       eventKind = "slot"       // a sanitize slot was acquired
	evNextStep   eventKind = "nextstep"   // the step was rejected and another exists
	evVerified   eventKind = "verified"   // the file was verified and committed
	evReverified eventKind = "reverified" // a recovered file passed verification
	evFail       eventKind = "fail"       // the job failed
	evEvict      eventKind = "evict"      // LRU eviction (no readers)
	evExpire     eventKind = "expire"     // a failure's until passed
	evShutdown   eventKind = "shutdown"   // shutdown dropped a queued job or ended a running one
	evInvalid    eventKind = "invalid"    // a recovered file failed verification
)

// event carries the facts a transition depends on.
type event struct {
	kind      eventKind
	queueRoom bool // the visitor queue has room (evVisitor)
	queueHalf bool // the visitor queue is at least half full (evPrewarm)
	reason    sanitize.FailReason
	until     time.Time
}

// effect is what the cache must do besides storing the next state.
type effect int

const (
	effNone    effect = iota
	effRefuse         // the request is refused: queue full, prewarm skipped or failed key
	effEnqueue        // a new job: queue it at the state's priority
	effPromote        // a queued prewarm job moves to the visitor queue
)

// transition is the only place job states change. It returns the next
// state and the queue effect; refused requests keep the state.
func transition(s State, e event) (State, effect, error) {
	switch s.Phase {
	case Absent:
		switch e.kind {
		case evVisitor:
			if !e.queueRoom {
				return s, effRefuse, nil
			}
			return State{Phase: Queued, Priority: Visitor}, effEnqueue, nil
		case evPrewarm:
			if e.queueHalf {
				return s, effRefuse, nil
			}
			return State{Phase: Queued, Priority: Prewarm}, effEnqueue, nil
		}
	case Queued:
		switch e.kind {
		case evVisitor:
			if s.Priority == Prewarm {
				return State{Phase: Queued, Priority: Visitor}, effPromote, nil
			}
			return s, effNone, nil
		case evPrewarm:
			return s, effNone, nil
		case evSlot:
			return State{Phase: Running, Priority: s.Priority}, effNone, nil
		case evShutdown:
			return State{Phase: Absent}, effNone, nil
		}
	case Running:
		switch e.kind {
		case evVisitor, evPrewarm:
			return s, effNone, nil
		case evNextStep:
			return State{Phase: Running, Priority: s.Priority, Step: s.Step + 1}, effNone, nil
		case evVerified:
			return State{Phase: Committed}, effNone, nil
		case evFail:
			return State{Phase: Failed, Reason: e.reason, Until: e.until}, effNone, nil
		case evShutdown:
			return State{Phase: Absent}, effNone, nil
		}
	case Committed:
		switch e.kind {
		case evVisitor, evPrewarm:
			return s, effNone, nil
		case evEvict:
			return State{Phase: Absent}, effNone, nil
		}
	case Recovered:
		switch e.kind {
		case evVisitor, evPrewarm:
			return s, effNone, nil
		case evReverified:
			return State{Phase: Committed}, effNone, nil
		case evEvict, evInvalid:
			return State{Phase: Absent}, effNone, nil
		}
	case Failed:
		switch e.kind {
		case evVisitor, evPrewarm:
			return s, effRefuse, nil
		case evExpire:
			return State{Phase: Absent}, effNone, nil
		}
	}
	return s, effRefuse, fmt.Errorf("cache: illegal event %s in state %s", e.kind, s.Phase)
}
