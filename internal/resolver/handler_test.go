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

// fakeRR is a Resolver that returns a preset response or error.
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
// written by Handler so tests can inspect the response message.
type captureWriter struct {
	got []byte
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
func (c *captureWriter) LocalAddr() net.Addr  { return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 53} }
func (c *captureWriter) RemoteAddr() net.Addr { return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234} }
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
