package blocklist_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func writeList(t *testing.T, dir, name string, entries ...string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	body := ""
	for _, e := range entries {
		body += e + "\n"
	}
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestLoader_LoadBuildsMatcher(t *testing.T) {
	dir := t.TempDir()
	p1 := writeList(t, dir, "a.txt", "ads.example.com", "foo.com")
	p2 := writeList(t, dir, "b.txt", "tracker.net", "ads.example.com") // dup OK

	ldr := blocklist.NewLoader([]blocklist.Source{
		blocklist.FileSource{Path: p1},
		blocklist.FileSource{Path: p2},
	})

	m, n, err := ldr.Load(context.Background())
	require.NoError(t, err)
	require.Equal(t, 3, m.Size())
	require.Equal(t, 4, n) // 4 raw entries, 3 unique
	require.True(t, m.Match("ads.example.com"))
	require.True(t, m.Match("x.tracker.net"))
}

func TestLoader_NoSourcesYieldsEmptyMatcher(t *testing.T) {
	ldr := blocklist.NewLoader(nil)
	m, n, err := ldr.Load(context.Background())
	require.NoError(t, err)
	require.NotNil(t, m)
	require.Equal(t, 0, m.Size())
	require.Equal(t, 0, n)
}

type failingSource struct{}

func (failingSource) Load(ctx context.Context) ([]string, error) {
	return nil, errors.New("nope")
}

func TestLoader_AnySourceFailingFailsLoad(t *testing.T) {
	ldr := blocklist.NewLoader([]blocklist.Source{failingSource{}})
	_, _, err := ldr.Load(context.Background())
	require.Error(t, err)
}
