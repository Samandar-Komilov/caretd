// Package clock provides an injectable time source for use in FSM and timer logic.
// Production code uses Real; tests use Fake to drive timers deterministically without
// real sleeps.
//
// Rule (STYLEGUIDE §4): never call time.Now() or time.After() in FSM/transaction logic —
// always inject a Clock.
package clock

import (
	"sync"
	"time"
)

// Timer wraps a time.Timer so it can be faked in tests.
type Timer interface {
	// C returns the channel that receives the tick when the timer fires.
	C() <-chan time.Time
	// Stop prevents the timer from firing. Returns true if stopped before firing.
	Stop() bool
}

// Clock is the injectable time source. FSMs and protocol timers receive this interface;
// the concrete type is wired only in main.go (or test setup).
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// realTimer wraps time.Timer to satisfy Timer.
type realTimer struct {
	t *time.Timer
}

func (r *realTimer) C() <-chan time.Time { return r.t.C }
func (r *realTimer) Stop() bool          { return r.t.Stop() }

// Real is the production Clock implementation backed by the system clock.
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// NewTimer returns a Timer that fires after duration d.
func (Real) NewTimer(d time.Duration) Timer {
	return &realTimer{t: time.NewTimer(d)}
}

// fakeTimer is owned by Fake and fires when Fake.Advance moves time past its deadline.
type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	stopped  bool
	mu       sync.Mutex
}

func (ft *fakeTimer) C() <-chan time.Time { return ft.ch }

// Stop prevents the timer from firing. Returns true if stopped before it fired.
func (ft *fakeTimer) Stop() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	if ft.stopped {
		return false
	}
	ft.stopped = true
	return true
}

// fire sends t on the timer's channel if not already stopped. Must not be called with
// ft.mu held.
func (ft *fakeTimer) fire(t time.Time) {
	ft.mu.Lock()
	stopped := ft.stopped
	if !stopped {
		ft.stopped = true // a one-shot timer fires at most once
	}
	ft.mu.Unlock()
	if !stopped {
		// Non-blocking send: if the receiver has already drained and is not waiting,
		// we still deliver. Channel capacity is 1 (same as time.Timer).
		ft.ch <- t
	}
}

// Fake is a manually-controlled Clock for use in tests. It is not safe for concurrent
// use across multiple goroutines without external synchronisation, but it is safe for
// a single test goroutine calling Advance/Set while other goroutines read from timer
// channels.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

// NewFake returns a Fake clock set to t.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t}
}

// Now returns the current fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the fake clock to t. If t is after current time any timers whose deadlines
// fall at or before t are fired in deadline order.
func (f *Fake) Set(t time.Time) {
	f.advance(t)
}

// Advance moves the fake clock forward by d. Any timers whose deadlines fall within
// [current+1ns, current+d] are fired in deadline order.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	f.mu.Unlock()
	f.advance(target)
}

// advance is the internal implementation: move to target and fire eligible timers.
func (f *Fake) advance(target time.Time) {
	for {
		f.mu.Lock()
		if !target.After(f.now) {
			// Already at or past target; nothing more to fire.
			f.mu.Unlock()
			return
		}

		// Find the earliest pending timer at or before target.
		var earliest *fakeTimer
		for _, ft := range f.timers {
			ft.mu.Lock()
			stopped := ft.stopped
			ft.mu.Unlock()
			if stopped {
				continue
			}
			if !ft.deadline.After(target) {
				if earliest == nil || ft.deadline.Before(earliest.deadline) {
					earliest = ft
				}
			}
		}

		if earliest == nil {
			// No pending timers before target; jump straight to target.
			f.now = target
			f.mu.Unlock()
			return
		}

		// Advance to the earliest timer's deadline and fire it.
		f.now = earliest.deadline
		now := f.now
		f.mu.Unlock()

		earliest.fire(now)
	}
}

// NewTimer returns a fakeTimer that will fire when the Fake clock is advanced past d
// from Now.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	deadline := f.now.Add(d)
	ft := &fakeTimer{
		deadline: deadline,
		ch:       make(chan time.Time, 1),
	}
	f.timers = append(f.timers, ft)
	f.mu.Unlock()
	return ft
}
