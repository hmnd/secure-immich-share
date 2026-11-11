// Package clock provides the injected time source. Use the Clock for
// anything tests must control; wall-clock I/O deadlines use time directly.
package clock

import (
	"sync"
	"time"
)

// Clock is the time source injected into every component.
type Clock interface {
	Now() time.Time
}

// Real is the production Clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake is a goroutine-safe Clock for tests.
type Fake struct {
	mu  sync.RWMutex
	now time.Time
}

// NewFake returns a Fake starting at t.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t}
}

func (f *Fake) Now() time.Time {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.now
}

func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
