package clock_test

import (
	"testing"
	"time"

	"github.com/Samandar-Komilov/caretd/internal/clock"
)

var epoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// TestFakeNow verifies that Now returns the time set at construction and after Set.
func TestFakeNow(t *testing.T) {
	c := clock.NewFake(epoch)
	if got := c.Now(); !got.Equal(epoch) {
		t.Fatalf("Now() = %v, want %v", got, epoch)
	}

	later := epoch.Add(5 * time.Second)
	c.Set(later)
	if got := c.Now(); !got.Equal(later) {
		t.Fatalf("after Set: Now() = %v, want %v", got, later)
	}
}

// TestFakeAdvanceFiresTimer verifies that a timer fires exactly when Advance crosses its
// deadline — not before, and exactly once.
func TestFakeAdvanceFiresTimer(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(10 * time.Second)

	// Advance by less than the timer duration — must not fire.
	c.Advance(5 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired too early")
	default:
	}

	// Advance past the deadline — must fire.
	c.Advance(6 * time.Second) // total 11s > 10s deadline
	select {
	case fired := <-timer.C():
		want := epoch.Add(10 * time.Second)
		if !fired.Equal(want) {
			t.Fatalf("fired at %v, want %v", fired, want)
		}
	default:
		t.Fatal("timer did not fire after crossing deadline")
	}

	// Must not fire a second time.
	c.Advance(10 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("timer fired a second time")
	default:
	}
}

// TestFakeMultipleTimersFiredInOrder verifies that multiple timers are delivered in
// deadline order when Advance skips past all of them.
func TestFakeMultipleTimersFiredInOrder(t *testing.T) {
	c := clock.NewFake(epoch)

	t1 := c.NewTimer(1 * time.Second)
	t2 := c.NewTimer(3 * time.Second)
	t3 := c.NewTimer(2 * time.Second)

	c.Advance(4 * time.Second) // crosses all three deadlines

	var got []time.Time
	for _, tmr := range []clock.Timer{t1, t2, t3} {
		select {
		case fired := <-tmr.C():
			got = append(got, fired)
		default:
			t.Fatal("expected all three timers to have fired")
		}
	}

	// t1 deadline = epoch+1, t3 deadline = epoch+2, t2 deadline = epoch+3
	want := []time.Time{
		epoch.Add(1 * time.Second),
		epoch.Add(3 * time.Second),
		epoch.Add(2 * time.Second),
	}
	for i, w := range want {
		if !got[i].Equal(w) {
			t.Errorf("timer[%d] fired at %v, want %v", i, got[i], w)
		}
	}
}

// TestFakeStopPreventsFireWhenStoppedBeforeAdvance verifies Stop works when called
// before the clock advances past the timer's deadline.
func TestFakeStopPreventsFireWhenStoppedBeforeAdvance(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(10 * time.Second)

	if stopped := timer.Stop(); !stopped {
		t.Fatal("Stop() should return true when timer has not fired")
	}

	c.Advance(20 * time.Second) // past deadline

	select {
	case <-timer.C():
		t.Fatal("stopped timer fired")
	default:
	}
}

// TestFakeStopAfterFireReturnsFalse verifies that Stop on an already-fired timer
// returns false.
func TestFakeStopAfterFireReturnsFalse(t *testing.T) {
	c := clock.NewFake(epoch)
	timer := c.NewTimer(1 * time.Second)
	c.Advance(2 * time.Second)

	// Drain the channel.
	select {
	case <-timer.C():
	default:
		t.Fatal("timer should have fired")
	}

	if stopped := timer.Stop(); stopped {
		t.Fatal("Stop() should return false after timer already fired")
	}
}

// TestRealClockSatisfiesInterface verifies Real satisfies the Clock interface.
func TestRealClockSatisfiesInterface(t *testing.T) {
	var c clock.Clock = clock.Real{}
	now := c.Now()
	if now.IsZero() {
		t.Fatal("Real.Now() returned zero time")
	}
	timer := c.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	if timer.C() == nil {
		t.Fatal("Real.NewTimer returned nil channel")
	}
}
