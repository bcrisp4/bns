package cache

import (
	"container/list"
	"sync"
	"time"

	"codeberg.org/miekg/dns"
)

// LRU is a bounded TTL-aware LRU cache for DNS responses.
//
// Invariants:
//   - Store deep-copies the input *dns.Msg before recording it, so the caller
//     may safely mutate the original after Store returns.
//   - Get returns a deep-copy of the stored *dns.Msg with TTLs in all RR
//     sections decremented by (now - storedAt), so downstream clients see the
//     remaining TTL rather than the original upstream TTL.
//   - Expired entries are evicted lazily on the next Get for that key.
//   - Capacity is enforced strictly: a Store on a full cache evicts the
//     least-recently-used entry first.
//
// Concurrency: a single Mutex guards both the linked list and the map.
// At MVP scale (few-thousand QPS on a Pi) contention is a non-issue; a
// single lock avoids the complexity of sharding or read/write split.
type LRU struct {
	mu       sync.Mutex
	capacity int
	list     *list.List // front == most recently used
	entries  map[string]*list.Element
}

// entry is the value stored in each list element.
type entry struct {
	key       string
	response  *dns.Msg
	storedAt  time.Time
	expiresAt time.Time
	negative  bool // true for NXDOMAIN/NODATA negative cache entries
}

// NewLRU constructs an LRU with the given maximum entry count.
// Capacity is clamped to at least 1.
func NewLRU(capacity int) *LRU {
	if capacity < 1 {
		capacity = 1
	}
	return &LRU{
		capacity: capacity,
		list:     list.New(),
		entries:  make(map[string]*list.Element, capacity),
	}
}

// Len returns the current number of entries in the cache.
func (c *LRU) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.list.Len()
}

// Capacity returns the configured maximum entry count.
func (c *LRU) Capacity() int { return c.capacity }

// Store inserts or replaces the entry for key. msg is deep-copied before
// storage; the caller may continue to use or mutate the original safely.
// ttl is the wall-clock lifetime of this entry; negative marks the entry
// as a negative cache result (NXDOMAIN/NODATA).
func (c *LRU) Store(key string, msg *dns.Msg, ttl time.Duration, negative bool) {
	now := time.Now()
	e := &entry{
		key:       key,
		response:  cloneMsg(msg), // deep-copy: caller owns the original
		storedAt:  now,
		expiresAt: now.Add(ttl),
		negative:  negative,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		// Update in place and promote to front.
		c.list.MoveToFront(el)
		el.Value = e
		return
	}

	el := c.list.PushFront(e)
	c.entries[key] = el

	// Evict the least-recently-used entry if we exceed capacity.
	for c.list.Len() > c.capacity {
		oldest := c.list.Back()
		if oldest == nil {
			break
		}
		c.list.Remove(oldest)
		delete(c.entries, oldest.Value.(*entry).key)
	}
}

// Get returns a deep-copy of the cached response for key, with TTLs
// decremented by the entry's age. ok=false is returned on a miss or if the
// entry has expired; expired entries are evicted on Get.
func (c *LRU) Get(key string) (*dns.Msg, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*entry)
	now := time.Now()
	if now.After(e.expiresAt) {
		// Lazy eviction: remove the expired entry on access.
		c.list.Remove(el)
		delete(c.entries, key)
		return nil, false
	}

	c.list.MoveToFront(el)

	// Return a deep-copy with TTLs decremented by age, so callers see the
	// remaining wire-format TTL rather than the upstream value.
	cp := cloneMsg(e.response)
	age := uint32(now.Sub(e.storedAt) / time.Second)
	decrementTTLs(cp, age)
	return cp, true
}

// cloneMsg returns a true deep-copy of a *dns.Msg by cloning each RR via
// its Clone() method. dns.Msg.Copy() in v2 is a shallow struct copy — the
// slice elements (RR pointers) are shared, so Header().TTL mutations on one
// copy would corrupt the other. We need independent RR values.
func cloneMsg(m *dns.Msg) *dns.Msg {
	cp := m.Copy() // copies MsgHeader and slice headers; elements still shared
	cp.Question = cloneRRs(m.Question)
	cp.Answer = cloneRRs(m.Answer)
	cp.Ns = cloneRRs(m.Ns)
	cp.Extra = cloneRRs(m.Extra)
	return cp
}

// cloneRRs deep-copies a slice of RRs using each RR's Clone method.
func cloneRRs(src []dns.RR) []dns.RR {
	if src == nil {
		return nil
	}
	dst := make([]dns.RR, len(src))
	for i, rr := range src {
		dst[i] = rr.Clone()
	}
	return dst
}

// decrementTTLs subtracts by from every RR TTL in all sections of m.
// TTLs floor at zero; they are never allowed to go negative.
func decrementTTLs(m *dns.Msg, by uint32) {
	for _, section := range [][]dns.RR{m.Question, m.Answer, m.Ns, m.Extra} {
		for _, rr := range section {
			h := rr.Header()
			if h.TTL > by {
				h.TTL -= by
			} else {
				h.TTL = 0
			}
		}
	}
}
