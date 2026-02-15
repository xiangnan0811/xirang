package auth

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type loginLockState struct {
	failCount   int
	lockedUntil time.Time
}

type LoginFailureLocker struct {
	mu           sync.Mutex
	states       map[string]loginLockState
	threshold    int
	lockDuration time.Duration
}

func NewLoginFailureLocker(threshold int, lockDuration time.Duration) *LoginFailureLocker {
	if threshold <= 0 {
		threshold = 5
	}
	if lockDuration <= 0 {
		lockDuration = 15 * time.Minute
	}
	return &LoginFailureLocker{
		states:       make(map[string]loginLockState),
		threshold:    threshold,
		lockDuration: lockDuration,
	}
}

func (l *LoginFailureLocker) IsLocked(username, ip string, now time.Time) (time.Time, bool) {
	key := l.buildKey(username, ip)

	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.states[key]
	if !ok {
		return time.Time{}, false
	}
	if entry.lockedUntil.After(now) {
		return entry.lockedUntil, true
	}
	if !entry.lockedUntil.IsZero() {
		delete(l.states, key)
	}
	return time.Time{}, false
}

func (l *LoginFailureLocker) RegisterFailure(username, ip string, now time.Time) {
	key := l.buildKey(username, ip)

	l.mu.Lock()
	defer l.mu.Unlock()

	entry := l.states[key]
	if entry.lockedUntil.After(now) {
		return
	}
	if !entry.lockedUntil.IsZero() && !entry.lockedUntil.After(now) {
		entry = loginLockState{}
	}

	entry.failCount++
	if entry.failCount >= l.threshold {
		entry.failCount = 0
		entry.lockedUntil = now.Add(l.lockDuration)
	}
	l.states[key] = entry
}

func (l *LoginFailureLocker) RegisterSuccess(username, ip string) {
	key := l.buildKey(username, ip)

	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.states, key)
}

func (l *LoginFailureLocker) buildKey(username, ip string) string {
	normalizedUsername := strings.ToLower(strings.TrimSpace(username))
	normalizedIP := strings.TrimSpace(ip)
	if normalizedIP == "" {
		normalizedIP = "unknown"
	}
	return fmt.Sprintf("%s|%s", normalizedUsername, normalizedIP)
}
