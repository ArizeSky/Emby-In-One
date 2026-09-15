package backend

import (
	"sync"
	"time"
)

const (
	loginMaxFailures   = 5
	loginLockoutWindow = 15 * time.Minute
	loginMaxTrackedIPs = 10000
)

// loginRateLimiter tracks per-IP login attempts.
type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*loginAttempt
}

type loginAttempt struct {
	failures int
	lastFail time.Time
}

func (a *loginAttempt) expired(now time.Time) bool {
	return now.Sub(a.lastFail) > loginLockoutWindow
}

func (a *loginAttempt) lockedOut() bool {
	return a.failures >= loginMaxFailures
}

// checkAndRecord atomically checks the rate limit and pre-records an attempt.
// Returns true if the request is allowed. On successful auth, call recordSuccess to clear.
//
// A full table never refuses a request. Refusing new IPs at capacity would let an
// attacker who can spoof proxy headers fill the table and lock every client out,
// so the table evicts instead of blocking.
func (l *loginRateLimiter) checkAndRecord(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]*loginAttempt)
	}
	attempt, ok := l.attempts[ip]
	if !ok {
		if len(l.attempts) >= loginMaxTrackedIPs {
			l.evictOne()
		}
		l.attempts[ip] = &loginAttempt{failures: 1, lastFail: time.Now()}
		return true
	}
	if attempt.expired(time.Now()) {
		attempt.failures = 1
		attempt.lastFail = time.Now()
		return true
	}
	if attempt.lockedOut() {
		return false
	}
	attempt.failures++
	attempt.lastFail = time.Now()
	return true
}

// evictOne frees a single slot for a new IP. An expired entry goes first, then the
// oldest entry that is not currently locked out, so a flood of spoofed IPs cannot
// clear a live lockout. When every entry is locked out, the oldest is dropped
// anyway to keep the table bounded.
func (l *loginRateLimiter) evictOne() {
	now := time.Now()
	oldestIP, oldest := "", time.Time{}
	evictableIP, evictable := "", time.Time{}
	for ip, attempt := range l.attempts {
		if attempt.expired(now) {
			delete(l.attempts, ip)
			return
		}
		if oldestIP == "" || attempt.lastFail.Before(oldest) {
			oldestIP, oldest = ip, attempt.lastFail
		}
		if !attempt.lockedOut() && (evictableIP == "" || attempt.lastFail.Before(evictable)) {
			evictableIP, evictable = ip, attempt.lastFail
		}
	}
	if evictableIP == "" {
		evictableIP = oldestIP
	}
	if evictableIP != "" {
		delete(l.attempts, evictableIP)
	}
}

func (l *loginRateLimiter) recordSuccess(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

func (l *loginRateLimiter) cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for ip, attempt := range l.attempts {
		if attempt.expired(now) {
			delete(l.attempts, ip)
		}
	}
}
