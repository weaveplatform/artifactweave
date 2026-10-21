package fakeupstream

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// ClientIdentity creates a test-only CA and client certificate for mTLS. Configure
// Options.TLS with the returned pool and RequireAndVerifyClientCert, then place the
// certificate in the client's TLS config. No keys are written to disk or logs.
func ClientIdentity(t testing.TB) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("fakeupstream CA key: %v", err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fakeupstream CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("fakeupstream CA certificate: %v", err)
	}
	clientKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("fakeupstream client key: %v", err)
	}
	client := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "fakeupstream client"},
		NotBefore:    ca.NotBefore,
		NotAfter:     ca.NotAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, client, ca, &clientKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("fakeupstream client certificate: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}))
	return tls.Certificate{Certificate: [][]byte{der, caDER}, PrivateKey: clientKey}, pool
}
