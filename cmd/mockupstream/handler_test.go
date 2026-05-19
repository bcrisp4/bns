package main

import (
	"context"
	"testing"
	"time"

	"codeberg.org/miekg/dns"
	"github.com/stretchr/testify/require"
)

func newReq(name string, qtype uint16) *dns.Msg {
	m := dns.NewMsg(name, qtype)
	m.ID = 0x1234
	return m
}

func TestHandler_A_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeA))

	require.True(t, resp.Response)
	require.Equal(t, uint16(0x1234), resp.ID)
	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)

	a, ok := resp.Answer[0].(*dns.A)
	require.True(t, ok)
	require.Equal(t, "192.0.2.1", a.A.String())
	require.Equal(t, uint32(300), a.Hdr.TTL)
}

func TestHandler_AAAA_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeAAAA))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	aaaa, ok := resp.Answer[0].(*dns.AAAA)
	require.True(t, ok)
	require.Equal(t, "2001:db8::1", aaaa.AAAA.String())
}

func TestHandler_NS_ReturnsCannedRecord(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeNS))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	ns, ok := resp.Answer[0].(*dns.NS)
	require.True(t, ok)
	require.Equal(t, "ns.mock.invalid.", ns.Ns)
}

func TestHandler_SOA_UsesNegTTLForMinimum(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 90 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeSOA))

	require.Equal(t, uint16(dns.RcodeSuccess), resp.Rcode)
	require.Len(t, resp.Answer, 1)
	soa, ok := resp.Answer[0].(*dns.SOA)
	require.True(t, ok)
	require.Equal(t, uint32(90), soa.Minttl)
}

func TestHandler_UnknownType_Refused(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeMX))

	require.Equal(t, uint16(dns.RcodeRefused), resp.Rcode)
	require.Empty(t, resp.Answer)
}

func TestHandler_DropRate1_ReturnsNil(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second, DropRate: 1.0})
	resp := h.Answer(context.Background(), newReq("example.test.", dns.TypeA))
	require.Nil(t, resp)
}

func TestHandler_NoQuestion_Refused(t *testing.T) {
	h := NewHandler(HandlerConfig{TTL: 300 * time.Second, NegTTL: 60 * time.Second})
	m := &dns.Msg{}
	m.ID = 1
	resp := h.Answer(context.Background(), m)
	require.Equal(t, uint16(dns.RcodeRefused), resp.Rcode)
}
