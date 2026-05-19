package blocklist_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bcrisp4/bns/internal/blocklist"
	"github.com/stretchr/testify/require"
)

func TestFileSource_LoadsDomainsAndHostsMix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "list.txt")
	require.NoError(t, os.WriteFile(path, []byte(
		"# header comment\n"+
			"example.com\n"+
			"0.0.0.0 ads.example.net\n"+
			"\n"+
			"; bind-style comment\n"+
			"127.0.0.1 tracker.example.org\n"+
			"BAD ENTRY WITH SPACE\n"+
			"foo.com\n",
	), 0o644))

	src := blocklist.FileSource{Path: path}
	got, err := src.Load(context.Background())
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		"example.com",
		"ads.example.net",
		"tracker.example.org",
		"foo.com",
	}, got)
}

func TestFileSource_MissingFileError(t *testing.T) {
	src := blocklist.FileSource{Path: "/does/not/exist.txt"}
	_, err := src.Load(context.Background())
	require.Error(t, err)
}
