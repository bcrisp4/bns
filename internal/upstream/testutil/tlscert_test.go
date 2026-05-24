package testutil_test

import (
	"crypto/x509"
	"net"
	"testing"
	"time"

	"github.com/bcrisp4/bns/internal/upstream/testutil"
	"github.com/stretchr/testify/require"
)

func TestNewTLSCert_HasRequestedSANs(t *testing.T) {
	cert := testutil.NewTLSCert(t, []string{"test.example", "127.0.0.1"})

	require.NotNil(t, cert.Leaf)
	leaf := cert.Leaf

	// DNS SAN present.
	require.Contains(t, leaf.DNSNames, "test.example")

	// IP SAN present.
	found := false
	for _, ip := range leaf.IPAddresses {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			found = true
			break
		}
	}
	require.True(t, found, "cert must contain IP SAN 127.0.0.1")
}

func TestNewTLSCert_ValidNow(t *testing.T) {
	cert := testutil.NewTLSCert(t, []string{"x"})
	now := time.Now()
	require.True(t, now.After(cert.Leaf.NotBefore))
	require.True(t, now.Before(cert.Leaf.NotAfter))
}

func TestNewTLSCert_PoolAccepts(t *testing.T) {
	// Smoke check that the returned cert verifies against a pool
	// containing only itself.
	cert := testutil.NewTLSCert(t, []string{"127.0.0.1"})
	pool := x509.NewCertPool()
	pool.AddCert(cert.Leaf)

	_, err := cert.Leaf.Verify(x509.VerifyOptions{
		Roots:   pool,
		DNSName: "", // skip name check; we test SAN above
	})
	require.NoError(t, err)
}
