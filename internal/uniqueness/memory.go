// Package uniqueness provides shared identity-claim primitives for queue drivers.
package uniqueness

import (
	"container/heap"
	"sync"
	"time"
)

// MemoryStore holds TTL claims within one queue backend instance.
// Its zero value is ready for use.
type MemoryStore struct {
	mu      sync.Mutex
	next    uint64
	entries map[string]memoryClaim
	expires memoryExpiryQueue
}

type memoryClaim struct {
	token     uint64
	expiresAt time.Time
	expiry    *memoryExpiry
}

type memoryExpiry struct {
	key       string
	token     uint64
	expiresAt time.Time
	index     int
}

type memoryExpiryQueue []*memoryExpiry

// Len returns the number of expirations waiting for reclamation.
func (q memoryExpiryQueue) Len() int { return len(q) }

// Less keeps the earliest claim at the head so reclamation cost follows expired entries rather than live cardinality.
func (q memoryExpiryQueue) Less(i, j int) bool { return q[i].expiresAt.Before(q[j].expiresAt) }

// Swap preserves heap indexes used to remove compensated claims immediately.
func (q memoryExpiryQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index = i
	q[j].index = j
}

// Push appends an expiration through container/heap.
func (q *memoryExpiryQueue) Push(value any) {
	expiry := value.(*memoryExpiry)
	expiry.index = len(*q)
	*q = append(*q, expiry)
}

// Pop removes the latest heap slot after container/heap moves the minimum there.
func (q *memoryExpiryQueue) Pop() any {
	old := *q
	last := len(old) - 1
	expiry := old[last]
	old[last] = nil
	expiry.index = -1
	*q = old[:last]
	return expiry
}

// Acquire claims key for ttl and returns an ownership token when no live claim exists.
func (s *MemoryStore) Acquire(key string, ttl time.Duration) (uint64, bool) {
	if key == "" || ttl <= 0 {
		return 0, false
	}
	now := time.Now()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	if current, ok := s.entries[key]; ok && current.expiresAt.After(now) {
		return 0, false
	}
	if s.entries == nil {
		s.entries = make(map[string]memoryClaim)
	}
	s.next++
	if s.next == 0 {
		s.next++
	}
	expiry := &memoryExpiry{key: key, token: s.next, expiresAt: now.Add(ttl)}
	heap.Push(&s.expires, expiry)
	s.entries[key] = memoryClaim{token: s.next, expiresAt: expiry.expiresAt, expiry: expiry}
	return s.next, true
}

// Release removes key only when token still owns its current claim.
func (s *MemoryStore) Release(key string, token uint64) {
	if key == "" || token == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ok := s.entries[key]; ok && current.token == token {
		heap.Remove(&s.expires, current.expiry.index)
		delete(s.entries, key)
	}
}

// pruneExpiredLocked reclaims every elapsed claim before evaluating a new acquisition.
func (s *MemoryStore) pruneExpiredLocked(now time.Time) {
	for len(s.expires) > 0 && !s.expires[0].expiresAt.After(now) {
		expiry := heap.Pop(&s.expires).(*memoryExpiry)
		if current, ok := s.entries[expiry.key]; ok && current.token == expiry.token {
			delete(s.entries, expiry.key)
		}
	}
}
