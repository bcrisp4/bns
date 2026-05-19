// Package testutil spins up in-process DNS servers for tests.
package testutil

import (
	"context"
	"io"
	"net"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/stretchr/testify/require"
)

// Handler is a per-query function used by Spawn.
type Handler func(req *dns.Msg) *dns.Msg

// Spawn starts a UDP + TCP DNS server bound to an ephemeral 127.0.0.1 port
// and returns the host:port string. The server is closed via t.Cleanup.
//
// Both UDP and TCP share the same Handler. NotifyStartedFunc is used to
// synchronise: Spawn blocks until both servers have completed their internal
// init() call, eliminating the data race between init() and Shutdown() that
// occurs when Shutdown is called before the goroutine reaches its ready state.
func Spawn(t *testing.T, h Handler) string {
	t.Helper()

	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := udpConn.LocalAddr().String()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)

	tcpLn, err := net.Listen("tcp", net.JoinHostPort(host, port))
	require.NoError(t, err)

	// v2 ResponseWriter has no WriteMsg; pack then copy bytes to satisfy io.Writer.
	handler := dns.HandlerFunc(func(_ context.Context, w dns.ResponseWriter, req *dns.Msg) {
		resp := h(req)
		if err := resp.Pack(); err != nil {
			return
		}
		_, _ = io.Copy(w, resp)
	})

	udpReady := make(chan struct{})
	tcpReady := make(chan struct{})

	udpSrv := &dns.Server{
		PacketConn: udpConn,
		Handler:    handler,
		// NotifyStartedFunc fires after init() completes, guaranteeing the
		// server's internal state is initialised before Shutdown may be called.
		NotifyStartedFunc: func(_ context.Context) { close(udpReady) },
	}
	tcpSrv := &dns.Server{
		Listener: tcpLn,
		Handler:  handler,
		NotifyStartedFunc: func(_ context.Context) { close(tcpReady) },
	}

	go func() { _ = udpSrv.ListenAndServe() }()
	go func() { _ = tcpSrv.ListenAndServe() }()

	// Wait for both servers to be fully initialised before returning.
	<-udpReady
	<-tcpReady

	t.Cleanup(func() {
		udpSrv.Shutdown(context.Background())
		tcpSrv.Shutdown(context.Background())
	})
	return addr
}
