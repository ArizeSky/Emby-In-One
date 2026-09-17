package backend

import (
	"sync"
	"testing"
	"time"
)

// FIX-12: restartHealthChecks used to take the old runner in one critical section and
// install the new one in another. Two concurrent restarts could interleave as
// "A stops -> B stops (no-op) -> B installs runnerB -> A installs runnerA", which
// overwrote runnerB without ever cancelling it. Its goroutine then kept running for
// the lifetime of the process, re-logging in to every offline upstream alongside the
// live runner. Each lost race leaked one more.
//
// The detector is the live-runner counter maintained by healthcheck.go: a runner that
// is superseded is cancelled and decrements it, while one dropped on the floor never
// does. Every test below therefore ends by shutting the pool down and asserting the
// counter returns to where it started.
func TestRestartHealthChecksConcurrentRestartsDoNotLeak(t *testing.T) {
	pool := &UpstreamPool{}
	timeouts := TimeoutsConfig{HealthInterval: 1}

	const storm = 64
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < storm; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pool.restartHealthChecks(timeouts)
		}()
	}
	// A concurrent stop is a normal part of the picture: config saves and shutdown
	// both call it, which is what widened the window in production.
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 16; i++ {
			pool.stopHealthChecks()
		}
	}()
	close(start)
	wg.Wait()

	// Install one final runner so there is a single, reachable runner to cancel.
	pool.restartHealthChecks(timeouts)
	pool.mu.RLock()
	final := pool.health
	pool.mu.RUnlock()
	if final == nil {
		t.Fatal("expected a runner to be installed after the final restart")
	}
	pool.stopHealthChecks()

	select {
	case <-final.done:
	case <-time.After(10 * time.Second):
		t.Fatal("runner goroutine did not exit: a restart leaked an uncancelled runner")
	}

	pool.mu.RLock()
	installed := pool.health
	pool.mu.RUnlock()
	if installed != nil {
		t.Fatal("stopHealthChecks left a runner installed")
	}
}

// A restart with a non-positive interval must stop the running checks rather than
// leave the previous runner in place, and must not install a nil-but-live runner.
func TestRestartHealthChecksDisabledIntervalStopsRunner(t *testing.T) {
	pool := &UpstreamPool{}
	pool.restartHealthChecks(TimeoutsConfig{HealthInterval: 1})

	pool.mu.RLock()
	first := pool.health
	pool.mu.RUnlock()
	if first == nil {
		t.Fatal("expected a runner for a positive interval")
	}

	pool.restartHealthChecks(TimeoutsConfig{HealthInterval: 0})

	pool.mu.RLock()
	after := pool.health
	pool.mu.RUnlock()
	if after != nil {
		t.Fatal("interval <= 0 must clear the runner")
	}
	select {
	case <-first.done:
	case <-time.After(10 * time.Second):
		t.Fatal("previous runner was not cancelled when the interval was disabled")
	}
}

// stopHealthChecks must be safe to call repeatedly and must drain the runner it
// cancels, so shutdown cannot leave a health-check goroutine behind.
func TestStopHealthChecksIsIdempotentAndDrains(t *testing.T) {
	pool := &UpstreamPool{}
	pool.restartHealthChecks(TimeoutsConfig{HealthInterval: 1})
	pool.stopHealthChecks()
	pool.stopHealthChecks()

	pool.mu.RLock()
	installed := pool.health
	pool.mu.RUnlock()
	if installed != nil {
		t.Fatal("runner still installed after stopHealthChecks")
	}
}

// healthRunnerCount reports how many health-check goroutines the process has running.
// "At most one at a time" is the property the fix restores: a runner that is superseded
// must be cancelled, so the count returns to where it started.
//
// It reads the counter the runner itself maintains (healthcheck.go). Assertions use a
// delta rather than an absolute value, because other tests in this binary may have left a
// pool running.
func healthRunnerCount() int { return liveHealthCheckRunnerCount() }

func waitForHealthRunners(t *testing.T, want int) bool {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if healthRunnerCount() == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return healthRunnerCount() == want
}

// The gap between "stop took away the old runner" and "install the new runner" inside
// restartHealthChecks is what leaks a runner. With a barrier both restarts enter that
// gap together: each takes the map slot as nil, each creates a runner, and the last
// writer wins. The other runner is no longer reachable from p.health, so nothing will
// ever cancel it, and its goroutine keeps re-logging in to every offline upstream for
// the life of the process. Serialising stop+install into one critical section makes
// the second restart supersede the first, and exactly one runner stays live.
//
// The detector is the live-runner counter maintained by healthcheck.go: disabling the
// interval cancels whatever p.health points at, so any runner the process still has
// running afterwards was orphaned by the race. Without the fix this reproduces on the
// first iteration; the wait is bounded so a failure reports the leak instead of
// timing out.
func TestRestartHealthChecksDoesNotLeakWhenRestartInterleaves(t *testing.T) {
	timeouts := TimeoutsConfig{HealthInterval: 5}

	// A runner superseded by an earlier test may still be on its way out, so wait for the
	// count to settle before attributing anything below to this test.
	if !waitForHealthRunners(t, 0) {
		t.Fatalf("a health-check runner from an earlier test never exited: %d still alive", healthRunnerCount())
	}

	for i := 0; i < 25; i++ {
		pool := &UpstreamPool{}

		var wg sync.WaitGroup
		barrier := make(chan struct{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrier
			pool.restartHealthChecks(timeouts)
		}()

		// Both restarts are released together, so they interleave inside the gap.
		close(barrier)
		pool.restartHealthChecks(timeouts)
		wg.Wait()

		// Shut the pool down the way a config save does and drain every reachable
		// runner. Anything left in the set was dropped on the floor.
		pool.restartHealthChecks(TimeoutsConfig{HealthInterval: 0})
		if !waitForHealthRunners(t, 0) {
			t.Fatalf("iteration %d: %d health-check runner(s) still alive after shutdown, want 0 — "+
				"a concurrent restart leaked an uncancelled runner", i, healthRunnerCount())
		}
	}
}
