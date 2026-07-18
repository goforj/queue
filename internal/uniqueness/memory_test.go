package uniqueness

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestMemoryStoreAcquireRelease verifies claims expire and compensation cannot delete a newer owner.
func TestMemoryStoreAcquireRelease(t *testing.T) {
	var store MemoryStore
	first, ok := store.Acquire("critical:job", 20*time.Millisecond)
	if !ok || first == 0 {
		t.Fatal("first claim was not acquired")
	}
	if _, duplicate := store.Acquire("critical:job", time.Second); duplicate {
		t.Fatal("live claim admitted a duplicate")
	}

	time.Sleep(25 * time.Millisecond)
	second, ok := store.Acquire("critical:job", time.Second)
	if !ok || second == first {
		t.Fatal("expired claim was not replaced by a new owner")
	}
	store.Release("critical:job", first)
	if _, duplicate := store.Acquire("critical:job", time.Second); duplicate {
		t.Fatal("stale compensation removed the current owner")
	}
	store.Release("critical:job", second)
	if _, ok := store.Acquire("critical:job", time.Second); !ok {
		t.Fatal("current owner could not release its claim")
	}
}

// TestMemoryStoreConcurrentAcquireHasOneOwner verifies instance-scoped callers cannot both cross one claim boundary.
func TestMemoryStoreConcurrentAcquireHasOneOwner(t *testing.T) {
	var store MemoryStore
	start := make(chan struct{})
	var wait sync.WaitGroup
	var winners atomic.Int32
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			if _, ok := store.Acquire("shared", time.Minute); ok {
				winners.Add(1)
			}
		}()
	}
	close(start)
	wait.Wait()
	if winners.Load() != 1 {
		t.Fatalf("concurrent claim winners = %d, want 1", winners.Load())
	}
}

// TestMemoryStoreRejectsInvalidClaims verifies callers cannot create unbounded or anonymous entries.
func TestMemoryStoreRejectsInvalidClaims(t *testing.T) {
	var store MemoryStore
	if token, ok := store.Acquire("", time.Second); ok || token != 0 {
		t.Fatalf("empty key claim = (%d, %t), want rejected", token, ok)
	}
	if token, ok := store.Acquire("job", 0); ok || token != 0 {
		t.Fatalf("zero TTL claim = (%d, %t), want rejected", token, ok)
	}
}

// TestMemoryStoreReclaimsUnrelatedExpiredClaims verifies high-cardinality identities do not remain resident forever.
func TestMemoryStoreReclaimsUnrelatedExpiredClaims(t *testing.T) {
	var store MemoryStore
	if _, ok := store.Acquire("expired-a", time.Millisecond); !ok {
		t.Fatal("expired-a claim was not acquired")
	}
	if _, ok := store.Acquire("expired-b", time.Millisecond); !ok {
		t.Fatal("expired-b claim was not acquired")
	}
	time.Sleep(2 * time.Millisecond)
	if _, ok := store.Acquire("live", time.Minute); !ok {
		t.Fatal("live claim was not acquired")
	}
	if len(store.entries) != 1 || len(store.expires) != 1 {
		t.Fatalf("resident claims = entries:%d expirations:%d, want 1/1", len(store.entries), len(store.expires))
	}
}
