// -------------------------------------------------------------------------------
// Test Certificates
//
// Author: Alex Freidah
//
// A CA on P-256 keys, valid for an hour either side of now. Server
// certificates carry 127.0.0.1 and localhost, where every test listens; client
// certificates carry the common name a test asks for.
// -------------------------------------------------------------------------------

package tlstest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// -------------------------------------------------------------------------
// CERTIFICATE AUTHORITY
// -------------------------------------------------------------------------

// CA is a certificate authority whose certificate is written to File, alone in
// Dir, so it can be loaded as a file or as a directory. What it issues is
// written beside Dir.
type CA struct {
	File string
	Dir  string

	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	dir  string
}

// NewCA mints a CA and writes its certificate to a temporary directory.
func NewCA(t testing.TB) *CA {
	t.Helper()

	key := newKey(t)
	template := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: "vagabond test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("CreateCertificate(CA) = %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate(CA) = %v", err)
	}

	dir := t.TempDir()
	caDir := filepath.Join(dir, "ca")

	if err := os.Mkdir(caDir, 0o700); err != nil {
		t.Fatalf("creating the CA directory: %v", err)
	}

	file := filepath.Join(caDir, "ca.pem")
	writePEM(t, file, "CERTIFICATE", der)

	return &CA{File: file, Dir: caDir, cert: cert, key: key, dir: dir}
}

// -------------------------------------------------------------------------
// ISSUING
// -------------------------------------------------------------------------

// Server signs a server certificate for 127.0.0.1 and localhost and returns
// its certificate and key files.
func (ca *CA) Server(t testing.TB) (certFile, keyFile string) {
	t.Helper()

	return ca.issue(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: "server"},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		DNSNames:    []string{"localhost"},
	})
}

// Client signs a client certificate for commonName and returns its
// certificate and key files.
func (ca *CA) Client(t testing.TB, commonName string) (certFile, keyFile string) {
	t.Helper()

	return ca.issue(t, &x509.Certificate{
		Subject:     pkix.Name{CommonName: commonName},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
}

// issue completes template, signs it, and writes the certificate and a new key
// beside the CA's, named for the common name.
func (ca *CA) issue(t testing.TB, template *x509.Certificate) (certFile, keyFile string) {
	t.Helper()

	template.SerialNumber = serial(t)
	template.NotBefore = time.Now().Add(-time.Hour)
	template.NotAfter = time.Now().Add(time.Hour)
	template.KeyUsage = x509.KeyUsageDigitalSignature

	key := newKey(t)
	name := template.Subject.CommonName

	der, err := x509.CreateCertificate(rand.Reader, template, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("CreateCertificate(%s) = %v", name, err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey() = %v", err)
	}

	certFile = filepath.Join(ca.dir, name+".pem")
	keyFile = filepath.Join(ca.dir, name+"-key.pem")
	writePEM(t, certFile, "CERTIFICATE", der)
	writePEM(t, keyFile, "EC PRIVATE KEY", keyDER)

	return certFile, keyFile
}

// -------------------------------------------------------------------------
// HELPERS
// -------------------------------------------------------------------------

// newKey generates a P-256 key.
func newKey(t testing.TB) *ecdsa.PrivateKey {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() = %v", err)
	}

	return key
}

// serial is a random 128-bit serial number.
func serial(t testing.TB) *big.Int {
	t.Helper()

	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial number: %v", err)
	}

	return n
}

// writePEM writes one PEM block to path.
func writePEM(t testing.TB, path, blockType string, der []byte) {
	t.Helper()

	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
