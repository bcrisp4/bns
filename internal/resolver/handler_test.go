package resolver_test

import (
	"context"
	"errors"
	"net"
	"testing"

	"codeberg.org/miekg/dns"
	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/stretchr/testify/require"
)

type fakeRR struct {
	resp *dns.Msg
	err  error
}

func (f fakeRR) Resolve(_ context.Context, req *dns.Msg) (*dns.Msg, error) {
	if f.err != nil {
		return nil, f.err
	}
	r := f.resp.Copy()
	r.ID = req.ID
	return r, nil
}

// captureWriter implements dns.ResponseWriter and captures the packed bytes
// written by Handler so tests can inspect the response message. Set local
// or remote to override the default UDP 127.0.0.1 stub addrs.
type captureWriter struct {
	got    []byte
	local  net.Addr
	remote net.Addr
}

func (c *captureWriter) Write(p []byte) (int, error) {
	c.got = append(c.got, p...)
	return len(p), nil
}

// msg unpacks and returns the captured DNS message, or nil if nothing was written.
//
// WriteTo on a non-UDP ResponseWriter prefixes the payload with a 2-byte
// TCP-style length field (RFC 1035 §4.2.2), so we skip those two bytes before
// handing the raw wire format to Unpack.
func (c *captureWriter) msg(t *testing.T) *dns.Msg {
	t.Helper()
	if len(c.got) < 2 {
		return nil
	}
	m := new(dns.Msg)
	m.Data = c.got[2:] // strip 2-byte TCP length prefix added by WriteTo
	require.NoError(t, m.Unpack())
	return m
}

// Stub methods to satisfy dns.ResponseWriter.
func (c *captureWriter) LocalAddr() net.Addr {
	if c.local != nil {
		return c.local
	}
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53}
}
func (c *captureWriter) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
}
func (c *captureWriter) Conn() net.Conn       { return nil }
func (c *captureWriter) Session() *dns.Session { return nil }
func (c *captureWriter) Close() error         { return nil }
func (c *captureWriter) Hijack()              {}

func TestHandler_WritesResolverResponse(t *testing.T) {
	ok := new(dns.Msg)
	ok.Response = true
	h := resolver.NewHandler(fakeRR{resp: ok})

	w := &captureWriter{}
	req := dns.NewMsg("example.com.", dns.TypeA)
	h.ServeDNS(context.Background(), w, req)

	got := w.msg(t)
	require.NotNil(t, got)
	require.True(t, got.Response)
}

func TestHandler_OnErrorWritesSERVFAIL(t *testing.T) {
	h := resolver.NewHandler(fakeRR{err: errors.New("boom")})

	w := &captureWriter{}
	req := dns.NewMsg("example.com.", dns.TypeA)
	h.ServeDNS(context.Background(), w, req)

	got := w.msg(t)
	require.NotNil(t, got)
	require.Equal(t, uint16(dns.RcodeServerFailure), got.Rcode)
}

func TestHandler_InstallsClientInfoFromWriter(t *testing.T) {
	cases := []struct {
		name      string
		local     net.Addr
		remote    net.Addr
		wantProto string
	}{
		{
			name:      "udp",
			local:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53},
			remote:    &net.UDPAddr{IP: net.ParseIP("192.0.2.5"), Port: 54321},
			wantProto: "udp",
		},
		{
			name:      "tcp",
			local:     &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53},
			remote:    &net.TCPAddr{IP: net.ParseIP("192.0.2.5"), Port: 54321},
			wantProto: "tcp",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var captured context.Context
			next := resolver.ResolverFunc(func(ctx context.Context, req *dns.Msg) (*dns.Msg, error) {
				captured = ctx
				r := new(dns.Msg)
				r.Response = true
				r.ID = req.ID
				return r, nil
			})
			h := resolver.NewHandler(next)

			w := &captureWriter{local: tc.local, remote: tc.remote}
			req := dns.NewMsg("example.com.", dns.TypeA)
			h.ServeDNS(context.Background(), w, req)

			require.NotNil(t, captured)
			info, ok := resolver.ClientInfoFrom(captured)
			require.True(t, ok)
			require.Equal(t, "192.0.2.5:54321", info.Addr)
			require.Equal(t, tc.wantProto, info.Proto)
		})
	}
}
