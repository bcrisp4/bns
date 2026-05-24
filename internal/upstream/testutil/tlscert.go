package testutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// NewTLSCert generates a self-signed leaf certificate suitable for use as
// httptest.Server's TLS cert in DoH client tests. hosts may mix DNS names
// and IP literals — the helper splits them into the appropriate SAN slots.
//
// The returned *tls.Certificate has Leaf populated so callers can build
// an *x509.CertPool from it for client-side trust without re-parsing.
func NewTLSCert(t *testing.T, hosts []string) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "bns-test"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
		Leaf:        leaf,
	}
}
