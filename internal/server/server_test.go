package server_test

import (
	"context"
	"net"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/bcrisp4/bns/internal/server"
	"github.com/stretchr/testify/require"
)

func TestServer_UDPAndTCPAnswerQueries(t *testing.T) {
	udpConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := udpConn.LocalAddr().String()
	host, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	tcpLn, err := net.Listen("tcp", net.JoinHostPort(host, port))
	require.NoError(t, err)

	r := resolver.ResolverFunc(func(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
		resp := new(dns.Msg)
		resp.Response = true
		resp.ID = req.ID
		resp.Question = req.Question
		rr, _ := dns.New("example.com. 60 IN A 1.2.3.4")
		resp.Answer = []dns.RR{rr}
		return resp, nil
	})

	srv := server.New(udpConn, tcpLn, resolver.NewHandler(r))
	go func() { _ = srv.Serve() }()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}()

	readyCtx, readyCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readyCancel()
	require.NoError(t, srv.Ready(readyCtx))

	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			// v2: dns.Client embeds *Transport; Net/Timeout are not struct fields.
			// Network is passed to Exchange directly; timeout lives in Transport.
			c := dns.NewClient()
			c.Transport.ReadTimeout = 2 * time.Second
			q := dns.NewMsg("example.com.", dns.TypeA)
			resp, _, err := c.Exchange(context.Background(), q, network, addr)
			require.NoError(t, err)
			require.Len(t, resp.Answer, 1)
		})
	}
}
