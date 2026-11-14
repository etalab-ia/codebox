package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// LoginAttempt is the short-lived state of one authorization-code flow. It is
// kept in memory only: the gateway has no database, and losing an in-flight
// login on restart is acceptable (the user simply retries).
type LoginAttempt struct {
	State        string
	Nonce        string
	CodeVerifier string
	// LoopbackPort is non-zero for the desktop flow: the callback redirects to
	// http://127.0.0.1:<port>/callback with the gateway session token.
	LoopbackPort int
	CreatedAt    time.Time
}

// AttemptStore holds in-flight logins with bounded size and TTL. It is a
// deliberate, small in-memory cache: losing it only aborts logins in progress.
type AttemptStore struct {
	mu       sync.Mutex
	attempts map[string]*LoginAttempt
	consumed map[string]time.Time
	ttl      time.Duration
	maxItems int
	now      func() time.Time
}

// NewAttemptStore builds an attempt store. A 10-minute TTL comfortably exceeds
// a ProConnect login with a second factor.
func NewAttemptStore(ttl time.Duration, maxItems int, now func() time.Time) *AttemptStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if maxItems <= 0 {
		maxItems = 4096
	}
	if now == nil {
		now = time.Now
	}
	return &AttemptStore{attempts: make(map[string]*LoginAttempt), ttl: ttl, maxItems: maxItems, now: now}
}

// Put stores an attempt keyed by its state value, evicting expired entries and
// refusing to grow without bound.
func (a *AttemptStore) Put(attempt *LoginAttempt) error {
	if attempt == nil || attempt.State == "" {
		return errors.New("login attempt requires a state value")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked()
	if len(a.attempts) >= a.maxItems {
		return errors.New("too many logins in progress; retry shortly")
	}
	attempt.CreatedAt = a.now().UTC()
	a.attempts[attempt.State] = attempt
	return nil
}

// Take removes and returns the attempt for state, so a state value is usable
// exactly once (CSRF and replay protection). It also sweeps expired entries.
func (a *AttemptStore) Take(state string) (*LoginAttempt, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked()
	attempt, ok := a.attempts[state]
	if !ok {
		return nil, false
	}
	delete(a.attempts, state)
	return attempt, true
}

// ConsumeOnce records that key has been redeemed, returning false when it was
// already used. It is the replay guard for single-use login tickets.
//
// The record lives in memory, so it is per-instance: two gateway replicas behind
// a load balancer would each keep their own set. That is acceptable while the
// service runs as a single container, and closing it fully requires a shared
// store. The blast radius is bounded either way, because a ticket is a
// short-lived bearer for an identity ProConnect already authenticated, and a
// second redemption yields the same subject rather than new access.
func (a *AttemptStore) ConsumeOnce(key string, ttl time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.evictExpiredLocked()
	if a.consumed == nil {
		a.consumed = make(map[string]time.Time)
	}
	now := a.now().UTC()
	if expiry, ok := a.consumed[key]; ok && now.Before(expiry) {
		return false
	}
	if len(a.consumed) >= a.maxItems*8 {
		for k, expiry := range a.consumed {
			if !now.Before(expiry) {
				delete(a.consumed, k)
			}
		}
	}
	a.consumed[key] = now.Add(ttl)
	return true
}

func (a *AttemptStore) evictExpiredLocked() {
	cutoff := a.now().Add(-a.ttl)
	for state, attempt := range a.attempts {
		if attempt.CreatedAt.Before(cutoff) {
			delete(a.attempts, state)
		}
	}
}

// RateLimiter is a fixed-window limiter keyed by an arbitrary string (client
// IP or verified subject). It bounds request volume on a public endpoint
// without any external dependency.
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*rateBucket
	limit   int
	window  time.Duration
	now     func() time.Time
	maxKeys int
}

type rateBucket struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter builds a limiter allowing limit requests per window per key.
func NewRateLimiter(limit int, window time.Duration, maxKeys int, now func() time.Time) *RateLimiter {
	if limit <= 0 {
		limit = 60
	}
	if window <= 0 {
		window = time.Minute
	}
	if maxKeys <= 0 {
		maxKeys = 8192
	}
	if now == nil {
		now = time.Now
	}
	return &RateLimiter{buckets: make(map[string]*rateBucket), limit: limit, window: window, maxKeys: maxKeys, now: now}
}

// Allow reports whether the key may proceed and, when it may not, how long to
// wait before retrying.
func (r *RateLimiter) Allow(key string) (bool, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	bucket, ok := r.buckets[key]
	if !ok || !now.Before(bucket.resetAt) {
		if !ok && len(r.buckets) >= r.maxKeys {
			r.sweepLocked(now)
			if len(r.buckets) >= r.maxKeys {
				// Fail closed: refuse to grow the map without bound.
				return false, r.window
			}
		}
		r.buckets[key] = &rateBucket{count: 1, resetAt: now.Add(r.window)}
		return true, 0
	}
	if bucket.count >= r.limit {
		return false, bucket.resetAt.Sub(now)
	}
	bucket.count++
	return true, 0
}

func (r *RateLimiter) sweepLocked(now time.Time) {
	for key, bucket := range r.buckets {
		if !now.Before(bucket.resetAt) {
			delete(r.buckets, key)
		}
	}
}

// hashPrefix returns a short, non-reversible digest used to build opaque
// identifiers from user-supplied values without storing the value itself.
func hashPrefix(value string) string {
	sum := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(sum[:12])
}
