package backend

import (
	"strconv"
	"testing"
	"time"
)

func TestLoginRateLimiterMaxCapacity(t *testing.T) {
	limiter := &loginRateLimiter{}
	for i := 0; i < loginMaxTrackedIPs; i++ {
		if !limiter.checkAndRecord("ip-" + strconv.Itoa(i)) {
			t.Fatalf("expected checkAndRecord to succeed for ip-%d", i)
		}
	}
	// A new IP must never be refused just because the table is full.
	if !limiter.checkAndRecord("ip-overflow") {
		t.Fatal("a new IP must not be refused at capacity (global lockout regression)")
	}
	if got := len(limiter.attempts); got > loginMaxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want at most %d", got, loginMaxTrackedIPs)
	}
	// Existing IP should still work
	if !limiter.checkAndRecord("ip-0") {
		t.Fatal("expected checkAndRecord to allow existing IP")
	}
}

// TestLoginRateLimiterKeepsLiveLockoutUnderFlood checks that filling the table
// cannot clear an active lockout, which would hand a brute-forcer free retries.
func TestLoginRateLimiterKeepsLiveLockoutUnderFlood(t *testing.T) {
	limiter := &loginRateLimiter{}
	const attacker = "203.0.113.9"
	for i := 0; i < loginMaxFailures; i++ {
		if !limiter.checkAndRecord(attacker) {
			t.Fatalf("attempt %d should have been allowed", i+1)
		}
	}
	if limiter.checkAndRecord(attacker) {
		t.Fatal("expected the attacker to be locked out")
	}
	// Pin the attacker as the oldest entry: with a coarse clock the flood below
	// can share its timestamp, which would make eviction order nondeterministic.
	limiter.attempts[attacker].lastFail = time.Now().Add(-time.Minute)

	for i := 0; i < loginMaxTrackedIPs+50; i++ {
		limiter.checkAndRecord("flood-" + strconv.Itoa(i))
	}
	if limiter.checkAndRecord(attacker) {
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
			limiter.checkAndRecord(ip)
		}
	}
	for i := 0; i < 10; i++ {
		limiter.checkAndRecord("late-" + strconv.Itoa(i))
	}
	if got := len(limiter.attempts); got > loginMaxTrackedIPs {
		t.Fatalf("tracked IPs = %d, want at most %d", got, loginMaxTrackedIPs)
	}
}
