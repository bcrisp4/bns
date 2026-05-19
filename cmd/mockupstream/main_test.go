package main_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/stretchr/testify/require"
)

// findFreePort grabs an ephemeral 127.0.0.1 port and returns the host:port string.
// It closes the listener immediately so the caller can re-bind; brief race window
// is acceptable in tests.
func findFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestMockUpstream_Binary_AnswersA(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "mockupstream")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	require.NoError(t, build.Run())

	addr := findFreePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--listen.udp", addr, "--listen.tcp", addr)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	c := dns.NewClient()
	c.Transport.ReadTimeout = 500 * time.Millisecond
	c.Transport.WriteTimeout = 500 * time.Millisecond
	req := dns.NewMsg("example.test.", dns.TypeA)

	deadline := time.Now().Add(5 * time.Second)
	var resp *dns.Msg
	var err error
	for time.Now().Before(deadline) {
		resp, _, err = c.Exchange(context.Background(), req, "udp", addr)
		if err == nil && resp != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Len(t, resp.Answer, 1)
	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", a.A.String())
}
