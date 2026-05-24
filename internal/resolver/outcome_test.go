package resolver_test

import (
	"context"
	"testing"

	"github.com/bcrisp4/bns/internal/resolver"
	"github.com/stretchr/testify/require"
)

func TestClientInfo_RoundTrip(t *testing.T) {
	ctx := resolver.WithClientInfo(context.Background(), resolver.ClientInfo{
		Addr:  "192.0.2.5:54321",
		Proto: "udp",
	})

	got, ok := resolver.ClientInfoFrom(ctx)
	require.True(t, ok)
	require.Equal(t, "192.0.2.5:54321", got.Addr)
	require.Equal(t, "udp", got.Proto)
}

func TestClientInfo_AbsentYieldsZero(t *testing.T) {
	got, ok := resolver.ClientInfoFrom(context.Background())
	require.False(t, ok)
	require.Equal(t, resolver.ClientInfo{}, got)
}

func TestUpstreamInfo_AbsentYieldsZero(t *testing.T) {
	info, ok := resolver.UpstreamInfoFrom(context.Background())
	require.False(t, ok)
	require.Equal(t, resolver.UpstreamInfo{}, info)
}

func TestUpstreamInfo_RoundTrip(t *testing.T) {
	ctx, ptr := resolver.WithUpstreamMarker(context.Background())
	require.NotNil(t, ptr)

	// Before mark: marker exists but is empty.
	info, ok := resolver.UpstreamInfoFrom(ctx)
	require.False(t, ok, "empty marker should report not-ok")
	require.Equal(t, resolver.UpstreamInfo{}, info)

	// After mark: marker carries the values.
	resolver.MarkUpstream(ctx, "https://cloudflare-dns.com/dns-query", "doh")

	info, ok = resolver.UpstreamInfoFrom(ctx)
	require.True(t, ok)
	require.Equal(t, "https://cloudflare-dns.com/dns-query", info.Name)
	require.Equal(t, "doh", info.Protocol)
}

func TestMarkUpstream_NoMarker_NoOp(t *testing.T) {
	// Safe to call on a ctx with no marker installed.
	resolver.MarkUpstream(context.Background(), "x", "udp")
	// No panic, no effect.
}
