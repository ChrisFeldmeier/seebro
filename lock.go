package main

import (
	"fmt"
	"sync"
	"time"
)

// TabLock represents an active lock held by an agent on a tab.
type TabLock struct {
	Owner     string    `json:"owner"`
	LockedAt  time.Time `json:"lockedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// lockManager handles tab locking with timeout-based deadlock prevention.
type lockManager struct {
	locks map[string]*TabLock // tabID → lock
	mu    sync.Mutex
}

const (
	defaultLockTimeout = 30 * time.Second
	maxLockTimeout     = 5 * time.Minute
)

func newLockManager() *lockManager {
	return &lockManager{locks: make(map[string]*TabLock)}
}

// Lock acquires a lock on a tab for the given owner. Returns error if already
// locked by a different owner (and not expired).
func (lm *lockManager) Lock(tabID, owner string, timeout time.Duration) error {
	if owner == "" {
		return fmt.Errorf("owner required")
	}
	if timeout <= 0 {
		timeout = defaultLockTimeout
	}
	if timeout > maxLockTimeout {
		timeout = maxLockTimeout
	}

	lm.mu.Lock()
	defer lm.mu.Unlock()

	if existing, ok := lm.locks[tabID]; ok {
		if time.Now().Before(existing.ExpiresAt) && existing.Owner != owner {
			return fmt.Errorf("tab locked by %q until %s", existing.Owner, existing.ExpiresAt.Format(time.RFC3339))
		}
	}

	lm.locks[tabID] = &TabLock{
		Owner:     owner,
		LockedAt:  time.Now(),
		ExpiresAt: time.Now().Add(timeout),
	}
	return nil
}

// Unlock releases a lock. Only the owner (or anyone after expiry) can unlock.
func (lm *lockManager) Unlock(tabID, owner string) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	existing, ok := lm.locks[tabID]
	if !ok {
		return nil // not locked, idempotent
	}

	if existing.Owner != owner && time.Now().Before(existing.ExpiresAt) {
		return fmt.Errorf("tab locked by %q, cannot unlock", existing.Owner)
	}

	delete(lm.locks, tabID)
	return nil
}

// Get returns the lock info for a tab, or nil if unlocked/expired.
func (lm *lockManager) Get(tabID string) *TabLock {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	lock, ok := lm.locks[tabID]
	if !ok {
		return nil
	}
	if time.Now().After(lock.ExpiresAt) {
		delete(lm.locks, tabID)
		return nil
	}
	return lock
}

// CheckAccess returns an error if the tab is locked by someone other than owner.
func (lm *lockManager) CheckAccess(tabID, owner string) error {
	lock := lm.Get(tabID)
	if lock == nil {
		return nil
	}
	if owner == "" || lock.Owner != owner {
		return fmt.Errorf("tab locked by %q until %s", lock.Owner, lock.ExpiresAt.Format(time.RFC3339))
	}
	return nil
}
