package main

import (
	"testing"
	"time"
)

func TestLockBasic(t *testing.T) {
	lm := newLockManager()

	// Lock succeeds
	if err := lm.Lock("tab1", "agent-a", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Same owner can re-lock (extend)
	if err := lm.Lock("tab1", "agent-a", 5*time.Second); err != nil {
		t.Fatal(err)
	}

	// Different owner blocked
	if err := lm.Lock("tab1", "agent-b", 5*time.Second); err == nil {
		t.Fatal("expected lock conflict")
	}

	// Unlock
	if err := lm.Unlock("tab1", "agent-a"); err != nil {
		t.Fatal(err)
	}

	// Now agent-b can lock
	if err := lm.Lock("tab1", "agent-b", 5*time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestLockExpiry(t *testing.T) {
	lm := newLockManager()

	if err := lm.Lock("tab1", "agent-a", 1*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	// Expired — different owner can now lock
	if err := lm.Lock("tab1", "agent-b", 5*time.Second); err != nil {
		t.Fatalf("expected expired lock to allow new owner: %v", err)
	}
}

func TestLockCheckAccess(t *testing.T) {
	lm := newLockManager()

	// No lock — anyone can access
	if err := lm.CheckAccess("tab1", "anyone"); err != nil {
		t.Fatal(err)
	}

	_ = lm.Lock("tab1", "agent-a", 5*time.Second)

	// Owner can access
	if err := lm.CheckAccess("tab1", "agent-a"); err != nil {
		t.Fatal(err)
	}

	// Non-owner blocked
	if err := lm.CheckAccess("tab1", "agent-b"); err == nil {
		t.Fatal("expected access denied")
	}

	// Empty owner blocked
	if err := lm.CheckAccess("tab1", ""); err == nil {
		t.Fatal("expected access denied for empty owner")
	}
}

func TestUnlockIdempotent(t *testing.T) {
	lm := newLockManager()

	// Unlocking a non-locked tab is fine
	if err := lm.Unlock("tab1", "anyone"); err != nil {
		t.Fatal(err)
	}
}

func TestUnlockWrongOwner(t *testing.T) {
	lm := newLockManager()
	_ = lm.Lock("tab1", "agent-a", 5*time.Second)

	if err := lm.Unlock("tab1", "agent-b"); err == nil {
		t.Fatal("expected unlock denied for wrong owner")
	}
}

func TestMaxTimeout(t *testing.T) {
	lm := newLockManager()
	_ = lm.Lock("tab1", "agent-a", 10*time.Minute) // exceeds max

	lock := lm.Get("tab1")
	if lock == nil {
		t.Fatal("expected lock")
	}
	// Should be capped to maxLockTimeout (5 min)
	maxExpiry := time.Now().Add(maxLockTimeout + time.Second)
	if lock.ExpiresAt.After(maxExpiry) {
		t.Fatalf("lock timeout not capped: expires %v", lock.ExpiresAt)
	}
}

func TestLockRequiresOwner(t *testing.T) {
	lm := newLockManager()
	if err := lm.Lock("tab1", "", 5*time.Second); err == nil {
		t.Fatal("expected error for empty owner")
	}
}
