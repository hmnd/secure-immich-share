package ratelimit

import (
	"slices"
	"sync"
	"time"

	"github.com/hmnd/secure-immich-share/server/internal/clock"
)

// Unlock throttle, per (client IP, share):
//
//	L0     5 attempts per minute
//	L1-L4  locked out for 1, 2, 5, 15 minutes
//
// 5 wrong passwords within 10 minutes moves up one level; a correct
// password resets to L0; each hour without failures drops one level. Per
// share (all IPs) there is a 30/min bucket on attempts with no lockout, so
// an attacker can slow one share's unlocks but can't lock everyone out.

const (
	maxLevel        = 4
	attemptsPerMin  = 5
	failuresToLevel = 5
	failureWindow   = 10 * time.Minute
	decayEvery      = time.Hour
	sharePerMinute  = 30
)

var lockout = [maxLevel + 1]time.Duration{0, time.Minute, 2 * time.Minute, 5 * time.Minute, 15 * time.Minute}

// tstate is one (IP, share) throttle state.
type tstate struct {
	level       int
	lockedUntil time.Time
	attempts    []time.Time // within the last minute
	failures    []time.Time // within failureWindow, at this level
	calm        time.Time   // start of the current failure-free period
}

type event int

const (
	evAttempt event = iota // an unlock attempt is about to reach Immich
	evFailure              // Immich said invalid_password
	evSuccess              // Immich accepted the password
)

// transition is the only place throttle state changes. For evAttempt it
// reports whether the attempt may proceed and otherwise how long to wait.
func transition(s tstate, e event, now time.Time) (tstate, bool, time.Duration) {
	s = decay(s, now)
	switch e {
	case evAttempt:
		if now.Before(s.lockedUntil) {
			return s, false, s.lockedUntil.Sub(now)
		}
		s.attempts = slices.DeleteFunc(slices.Clone(s.attempts), func(t time.Time) bool { return now.Sub(t) >= time.Minute })
		if len(s.attempts) >= attemptsPerMin {
			return s, false, s.attempts[0].Add(time.Minute).Sub(now)
		}
		s.attempts = append(s.attempts, now)
		return s, true, 0
	case evFailure:
		s.failures = slices.DeleteFunc(slices.Clone(s.failures), func(t time.Time) bool { return now.Sub(t) >= failureWindow })
		s.failures = append(s.failures, now)
		s.calm = now
		if len(s.failures) >= failuresToLevel {
			s.level = min(s.level+1, maxLevel)
			s.lockedUntil = now.Add(lockout[s.level])
			s.failures = nil
			s.attempts = nil
			s.calm = s.lockedUntil
		}
		return s, true, 0
	case evSuccess:
		// Back to L0; the attempts still count toward the per-minute limit.
		return tstate{attempts: s.attempts}, true, 0
	}
	// Unknown events are refused and change nothing.
	return s, false, 0
}

// decay drops one level per full hour without failures.
func decay(s tstate, now time.Time) tstate {
	if s.level == 0 || s.calm.IsZero() || now.Before(s.calm) {
		return s
	}
	hours := int(now.Sub(s.calm) / decayEvery)
	if hours == 0 {
		return s
	}
	s.level = max(0, s.level-hours)
	s.calm = s.calm.Add(time.Duration(hours) * decayEvery)
	return s
}

func (s tstate) idle(now time.Time) bool {
	return s.level == 0 && now.After(s.lockedUntil) &&
		(len(s.failures) == 0 || now.Sub(s.failures[len(s.failures)-1]) >= failureWindow) &&
		(len(s.attempts) == 0 || now.Sub(s.attempts[len(s.attempts)-1]) >= time.Minute)
}

// Throttle is the unlock throttle, keyed by client IP and the keys naming
// a share. An attempt counts against all of a share's keys or none.
type Throttle struct {
	clock clock.Clock
	share *Buckets

	mu    sync.Mutex
	state map[[2]string]tstate
	sweep time.Time
}

func NewThrottle(c clock.Clock) *Throttle {
	return &Throttle{
		clock: c,
		share: NewBuckets(c, sharePerMinute/60.0, sharePerMinute),
		state: map[[2]string]tstate{},
	}
}

// Allow records an unlock attempt on every key, or on none when any of
// them refuses it; when refused it returns the wait.
func (t *Throttle) Allow(ip string, keys ...string) (bool, time.Duration) {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	t.gc(now)
	next := make([]tstate, len(keys))
	var wait time.Duration
	for i, key := range keys {
		s, ok, w := transition(t.state[[2]string{ip, key}], evAttempt, now)
		if !ok {
			wait = max(wait, w)
		}
		next[i] = s
	}
	if wait > 0 {
		return false, wait
	}
	if ok, wait := t.share.AllowAll(keys...); !ok {
		return false, wait
	}
	for i, key := range keys {
		t.state[[2]string{ip, key}] = next[i]
	}
	return true, 0
}

// Fail records a wrong password on every key.
func (t *Throttle) Fail(ip string, keys ...string) { t.apply(ip, evFailure, keys) }

// Succeed records a correct password on every key.
func (t *Throttle) Succeed(ip string, keys ...string) { t.apply(ip, evSuccess, keys) }

func (t *Throttle) apply(ip string, e event, keys []string) {
	now := t.clock.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, key := range keys {
		k := [2]string{ip, key}
		next, _, _ := transition(t.state[k], e, now)
		if next.idle(now) {
			delete(t.state, k)
			continue
		}
		t.state[k] = next
	}
}

func (t *Throttle) gc(now time.Time) {
	if now.Sub(t.sweep) < time.Minute {
		return
	}
	t.sweep = now
	for k, s := range t.state {
		if decay(s, now).idle(now) {
			delete(t.state, k)
		}
	}
}
