package blocklist_test

import (
	"sync"
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestHolder_SwapVisibleToReaders(t *testing.T) {
	h := blocklist.NewHolder(blocklist.NewMatcher([]string{"old.example"}))
	require.True(t, h.Current().Match("old.example"))

	h.Swap(blocklist.NewMatcher([]string{"new.example"}))
	require.False(t, h.Current().Match("old.example"))
	require.True(t, h.Current().Match("new.example"))
}

func TestHolder_ConcurrentReadsDuringSwap(t *testing.T) {
	h := blocklist.NewHolder(blocklist.NewMatcher([]string{"a.example"}))

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			_ = h.Current().Match("a.example")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			h.Swap(blocklist.NewMatcher([]string{"a.example"}))
		}
	}()
	wg.Wait()
}
