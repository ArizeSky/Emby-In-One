package backend

import (
	"strconv"
	"testing"
	"time"
)

func TestLoginRateLimiterCountsOnlyRealFailures(t *testing.T) {
	limiter := &loginRateLimiter{}

	// allowed() is read-only: probing an unknown IP must not create an entry,
	// so requests that never reach the credential check cannot burn the budget.
	if !limiter.allowed("203.0.113.1") {
		t.Fatal("an unknown IP must be allowed")
	}
	if len(limiter.attempts) != 0 {
		t.Fatalf("allowed() must not create entries, got %d", len(limiter.attempts))
	}

	// Lockout is reached only after loginMaxFailures real failures.
	for i := 1; i <= loginMaxFailures-1; i++ {
		limiter.recordFailure("203.0.113.1")
		if !limiter.allowed("203.0.113.1") {
			t.Fatalf("allowed after %d real failures (max %d)", i, loginMaxFailures)
		}
	}
	limiter.recordFailure("203.0.113.1")
	if limiter.allowed("203.0.113.1") {
		t.Fatal("expected lockout after loginMaxFailures real failures")
	}
}

// TestLoginRateLimiterExpiredEntryIsAllowed pins the regression where the expiry
// check in allowed() was inverted: an entry whose lockout window had elapsed —
// even a locked-out one — kept answering 429 until the periodic cleanup removed
// it, so a single forgotten failure could lock an IP out for up to 30 minutes.
func TestLoginRateLimiterExpiredEntryIsAllowed(t *testing.T) {
	limiter := &loginRateLimiter{}

	// A single failure, long ago.
	limiter.recordFailure("203.0.113.2")
	limiter.attempts["203.0.113.2"].lastFail = time.Now().Add(-(loginLockoutWindow + time.Minute))
	if !limiter.allowed("203.0.113.2") {
		t.Fatal("an entry past the lockout window must be allowed (expired-entry regression)")
	}

	// A full lockout, equally stale.
	locked := "203.0.113.3"
	for i := 0; i < loginMaxFailures; i++ {
		limiter.recordFailure(locked)
	}
	limiter.attempts[locked].lastFail = time.Now().Add(-(loginLockoutWindow + time.Minute))
	if !limiter.allowed(locked) {
		t.Fatal("a lockout past its window must be allowed (expired-entry regression)")
	}
}

func TestLoginRateLimiterRecordFailureResetsExpiredWindow(t *testing.T) {
	limiter := &loginRateLimiter{}
	limiter.recordFailure("203.0.113.4")
	limiter.attempts["203.0.113.4"].lastFail = time.Now().Add(-(loginLockoutWindow + time.Minute))

	// A failure after the window elapsed starts a fresh count, not a continuation.
	limiter.recordFailure("203.0.113.4")
	if got := limiter.attempts["203.0.113.4"].failures; got != 1 {
		t.Fatalf("failures after window reset = %d, want 1", got)
	}
	if !limiter.allowed("203.0.113.4") {
		t.Fatal("a single fresh failure must not lock out")
	}
}

func TestLoginRateLimiterRecordSuccessClears(t *testing.T) {
	limiter := &loginRateLimiter{}
	for i := 0; i < loginMaxFailures; i++ {
		limiter.recordFailure("203.0.113.5")
	}
	limiter.recordSuccess("203.0.113.5")
	if !limiter.allowed("203.0.113.5") {
		t.Fatal("a successful login must clear the failure history")
	}
	if _, ok := limiter.attempts["203.0.113.5"]; ok {
		t.Fatal("recordSuccess must remove the entry")
	}
}

func TestLoginRateLimiterMaxCapacity(t *testing.T) {
	limiter := &loginRateLimiter{}
	for i := 0; i < loginMaxTrackedIPs; i++ {
		limiter.recordFailure("ip-" + strconv.Itoa(i))
	}
	// A new IP must never be refused just because the table is full.
	if !limiter.allowed("ip-overflow") {
		t.Fatal("a new IP must not be refused at capacity (global lockout regression)")
	}
	limiter.recordFailure("ip-overflow")
	if got := len(limiter.attempts); got > loginMaxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want at most %d", got, loginMaxTrackedIPs)
	}
	// Existing IP should still work
	if !limiter.allowed("ip-0") {
		t.Fatal("expected an existing non-locked IP to be allowed")
	}
}

// TestLoginRateLimiterKeepsLiveLockoutUnderFlood checks that filling the table
// cannot clear an active lockout, which would hand a brute-forcer free retries.
func TestLoginRateLimiterKeepsLiveLockoutUnderFlood(t *testing.T) {
	limiter := &loginRateLimiter{}
	const attacker = "203.0.113.9"
	for i := 0; i < loginMaxFailures; i++ {
		limiter.recordFailure(attacker)
	}
	if limiter.allowed(attacker) {
		t.Fatal("expected the attacker to be locked out")
	}
	// Pin the attacker as the oldest entry: with a coarse clock the flood below
	// can share its timestamp, which would make eviction order nondeterministic.
	limiter.attempts[attacker].lastFail = time.Now().Add(-time.Minute)

	for i := 0; i < loginMaxTrackedIPs+50; i++ {
		limiter.recordFailure("flood-" + strconv.Itoa(i))
	}
	if limiter.allowed(attacker) {
		t.Fatal("filling the table must not reset an active lockout")
	}
}

// TestLoginRateLimiterStaysBoundedWhenEveryEntryIsLocked covers the fallback path
// where no evictable entry is left, so the table cannot grow without bound.
func TestLoginRateLimiterStaysBoundedWhenEveryEntryIsLocked(t *testing.T) {
	limiter := &loginRateLimiter{}
	for i := 0; i < loginMaxTrackedIPs; i++ {
		ip := "locked-" + strconv.Itoa(i)
		for j := 0; j < loginMaxFailures; j++ {
			limiter.recordFailure(ip)
		}
	}
	for i := 0; i < 10; i++ {
		limiter.recordFailure("late-" + strconv.Itoa(i))
	}
	if got := len(limiter.attempts); got > loginMaxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want at most %d", got, loginMaxTrackedIPs)
	}
}
