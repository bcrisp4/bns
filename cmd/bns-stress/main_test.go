//go:build stress_integration

package main_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/stress"
	_ "github.com/bcrisp4/bns/internal/stress/scenarios"
	"github.com/stretchr/testify/require"
)

func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestStress_MiniRun(t *testing.T) {
	cwd, err := os.Getwd()
	require.NoError(t, err)
	repoRoot := filepath.Join(cwd, "..", "..")
	absRepoRoot, err := filepath.Abs(repoRoot)
	require.NoError(t, err)

	binDir := filepath.Join(absRepoRoot, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	for _, target := range []struct{ out, pkg string }{
		{filepath.Join(binDir, "bns"), "./cmd/bns"},
		{filepath.Join(binDir, "mockupstream"), "./cmd/mockupstream"},
	} {
		build := exec.Command("go", "build", "-trimpath", "-o", target.out, target.pkg)
		build.Dir = absRepoRoot
		build.Stdout = os.Stdout
		build.Stderr = os.Stderr
		require.NoErrorf(t, build.Run(), "build %s", target.pkg)
	}

	// dnspyre resolves @<path> relative to cwd. Change to repo root so the
	// scenario's @scripts/stress/queries/mixed.txt resolves.
	t.Chdir(absRepoRoot)

	target := freePort(t)
	admin := freePort(t)
	outDir := t.TempDir()

	cfg := stress.Defaults()
	cfg.Scenario = "mixed"
	cfg.Target = target
	cfg.Admin = admin
	cfg.Duration = 2 * time.Second
	cfg.Concurrency = 4
	cfg.OutDir = outDir
	cfg.BNSBin = filepath.Join(binDir, "bns")
	cfg.MockBin = filepath.Join(binDir, "mockupstream")
	cfg.PprofCPU = 1 * time.Second
	cfg.PprofHeap = true

	// Tiny blocklist suffices for the smoke; full hagezi pro.txt is too heavy.
	blocklist := filepath.Join(outDir, "blocklist.txt")
	require.NoError(t, os.WriteFile(blocklist, []byte("blocked.test\n"), 0o644))
	cfg.BlocklistPath = blocklist
	// dnspyre counts context-cancelled in-flight queries at end of duration
	// window as IOErrors. In a short 2s smoke run a handful are expected;
	// tolerate up to concurrency count × 2 to avoid flakiness.
	cfg.MaxIOErrors = int64(cfg.Concurrency) * 2

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := stress.Run(ctx, cfg)
	require.NoError(t, err)
	require.Greater(t, res.TotalQueries, int64(0))
	require.FileExists(t, filepath.Join(outDir, "report.md"))
	require.FileExists(t, filepath.Join(outDir, "before.prom"))
	require.FileExists(t, filepath.Join(outDir, "after.prom"))
	require.FileExists(t, filepath.Join(outDir, "dnspyre-results.json"))
	require.FileExists(t, filepath.Join(outDir, "cpu.pprof"))
	require.FileExists(t, filepath.Join(outDir, "heap.pprof"))
	require.NoFileExists(t, filepath.Join(outDir, "FAILED"))
}
