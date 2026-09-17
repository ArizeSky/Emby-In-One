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

// allowed reports whether ip may attempt a login. It only reads: an attempt is counted
// once it has actually failed, so a request that never reaches the credential check — a
// cross-origin POST from any web page, a malformed body — cannot spend the budget of the
// IP it came from and lock that client out. An entry whose window has expired never
// blocks either: the lockout only lives as long as the failures stay fresh, and the
// entry is refreshed by the next recordFailure or dropped by the periodic cleanup.
func (l *loginRateLimiter) allowed(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempt, ok := l.attempts[ip]
	if !ok {
		return true
	}
	return attempt.expired(time.Now()) || !attempt.lockedOut()
}

// recordFailure counts one failed login attempt against ip, starting the lockout window.
//
// A full table never refuses a request. Refusing new IPs at capacity would let an
// attacker who can spoof proxy headers fill the table and lock every client out,
// so the table evicts instead of blocking.
func (l *loginRateLimiter) recordFailure(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.attempts == nil {
		l.attempts = make(map[string]*loginAttempt)
	}
	now := time.Now()
	attempt, ok := l.attempts[ip]
	if !ok {
		if len(l.attempts) >= loginMaxTrackedIPs {
			l.evictOne()
		}
		l.attempts[ip] = &loginAttempt{failures: 1, lastFail: now}
		return
	}
	if attempt.expired(now) {
		attempt.failures = 1
	} else {
		attempt.failures++
	}
	attempt.lastFail = now
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
