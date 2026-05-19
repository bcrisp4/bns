// Package server runs the BNS DNS listeners (UDP and TCP), both sharing
// one dns.Handler.
package server

import (
	"context"
	"errors"
	"net"

	"codeberg.org/miekg/dns"
	"golang.org/x/sync/errgroup"
)

// Server runs UDP and TCP DNS listeners.
type Server struct {
	udp       *dns.Server
	tcp       *dns.Server
	udpReady  chan struct{}
	tcpReady  chan struct{}
}

// New constructs a Server using pre-bound listeners. The same handler is
// invoked from both listeners.
func New(udpConn net.PacketConn, tcpLn net.Listener, h dns.Handler) *Server {
	udpReady := make(chan struct{})
	tcpReady := make(chan struct{})
	return &Server{
		udp: &dns.Server{
			PacketConn:        udpConn,
			Handler:           h,
			NotifyStartedFunc: func(_ context.Context) { close(udpReady) },
		},
		tcp: &dns.Server{
			Listener:          tcpLn,
			Handler:           h,
			NotifyStartedFunc: func(_ context.Context) { close(tcpReady) },
		},
		udpReady: udpReady,
		tcpReady: tcpReady,
	}
}

// Serve runs both listeners until either errors or Shutdown is called.
// Returns the first non-nil error other than the normal-shutdown ones.
func (s *Server) Serve() error {
	g := new(errgroup.Group)
	g.Go(func() error { return ignoreClosed(s.udp.ListenAndServe()) })
	g.Go(func() error { return ignoreClosed(s.tcp.ListenAndServe()) })
	return g.Wait()
}

// Ready blocks until both listeners have completed their internal init()
// and are actually serving. Callers must invoke Serve in a goroutine first.
// Returns when both servers are ready or ctx expires.
func (s *Server) Ready(ctx context.Context) error {
	select {
	case <-s.udpReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.tcpReady:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Shutdown stops both listeners. ctx bounds the wait for in-flight handlers.
// dns.Server.Shutdown returns no error in v2, so we always return nil.
//
// Callers must wait for Ready before calling Shutdown, otherwise the server's
// internal init() may race with Shutdown's read of the same fields.
func (s *Server) Shutdown(ctx context.Context) error {
	s.udp.Shutdown(ctx)
	s.tcp.Shutdown(ctx)
	return nil
}

func ignoreClosed(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}
