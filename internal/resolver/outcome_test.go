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
