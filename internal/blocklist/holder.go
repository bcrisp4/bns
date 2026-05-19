package blocklist

import "sync/atomic"

// Holder holds the currently-active Matcher behind an atomic pointer.
// Readers call Current() (lock-free); reloaders call Swap() once per
// successful reload. The previous Matcher is released to GC.
type Holder struct {
	m atomic.Pointer[Matcher]
}

// NewHolder seeds the holder with an initial Matcher.
func NewHolder(initial *Matcher) *Holder {
	h := &Holder{}
	h.m.Store(initial)
	return h
}

// Current returns the active Matcher. The returned pointer is read-only.
func (h *Holder) Current() *Matcher {
	return h.m.Load()
}

// Swap atomically replaces the active Matcher with next.
func (h *Holder) Swap(next *Matcher) {
	h.m.Store(next)
}
