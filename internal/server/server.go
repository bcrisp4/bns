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
	udp *dns.Server
	tcp *dns.Server
}

// New constructs a Server using pre-bound listeners. The same handler is
// invoked from both listeners.
func New(udpConn net.PacketConn, tcpLn net.Listener, h dns.Handler) *Server {
	return &Server{
		udp: &dns.Server{PacketConn: udpConn, Handler: h},
		tcp: &dns.Server{Listener: tcpLn, Handler: h},
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

// Shutdown stops both listeners. ctx bounds the wait for in-flight handlers.
// dns.Server.Shutdown returns no error in v2, so we always return nil.
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
