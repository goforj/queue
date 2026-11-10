package temporalqueue

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// tlsFixtureCertificate creates test-only trust and a certificate usable on both sides of a mutual TLS handshake.
func tlsFixtureCertificate(t *testing.T) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "queue test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, public, key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := x509.ParseCertificate(raw)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"localhost"}, Subject: pkix.Name{CommonName: "queue test peer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}}
	raw, err = x509.CreateCertificate(rand.Reader, leafTemplate, root, public, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{raw, root.Raw}, PrivateKey: key}, root
}

// tlsFixtureSigner separates signing failures from valid public material captured at configuration time.
type tlsFixtureSigner struct {
	crypto.Signer
	mode   string
	public crypto.PublicKey
	calls  int
}

// Public permits invalid and mismatched key configuration cases without corrupting real certificate bytes.
func (s *tlsFixtureSigner) Public() crypto.PublicKey {
	if s.public != nil {
		return s.public
	}
	return s.Signer.Public()
}

// Sign exposes controlled key-provider errors and panics through an actual crypto.Signer boundary.
func (s *tlsFixtureSigner) Sign(random io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	s.calls++
	if s.mode == "panic" {
		panic("private signing diagnostic")
	}
	if s.mode == "error" {
		return nil, errors.New("private signing diagnostic")
	}
	return s.Signer.Sign(random, digest, options)
}

// TestGuardedTLSMutualHandshake verifies cloned trust, dynamic certificate selection and real guarded private-key signing.
func TestGuardedTLSMutualHandshake(t *testing.T) {
	certificate, root := tlsFixtureCertificate(t)
	trust := x509.NewCertPool()
	trust.AddCert(root)
	signer := &tlsFixtureSigner{Signer: certificate.PrivateKey.(crypto.Signer)}
	certificate.PrivateKey = signer
	certificateCalls, peerCalls, connectionCalls := 0, 0, 0
	source := &tls.Config{ServerName: "localhost", RootCAs: trust, ClientCAs: trust, MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		certificateCalls++
		return &certificate, nil
	}, VerifyPeerCertificate: func(raw [][]byte, verified [][]*x509.Certificate) error {
		peerCalls++
		if len(raw) == 0 || len(verified) == 0 {
			return errors.New("missing verified peer")
		}
		return nil
	}, VerifyConnection: func(state tls.ConnectionState) error {
		connectionCalls++
		if state.Version != tls.VersionTLS13 {
			return errors.New("unexpected TLS version")
		}
		return nil
	}}
	gate := newAdmission()
	guarded, err := guardedTLS(source, gate)
	if err != nil {
		t.Fatal(err)
	}
	if guarded.RootCAs == source.RootCAs || guarded.ClientCAs == source.ClientCAs {
		t.Fatal("trust pools were not snapshotted")
	}
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverCertificate := certificate
	serverCertificate.PrivateKey = signer.Signer
	server := tls.Server(serverSide, &tls.Config{Certificates: []tls.Certificate{serverCertificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: trust, MinVersion: tls.VersionTLS13})
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.HandshakeContext(ctx) }()
	client := tls.Client(clientSide, guarded)
	if err := client.HandshakeContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if certificateCalls != 1 || peerCalls != 1 || connectionCalls != 1 || signer.calls != 1 {
		t.Fatalf("callbacks: certificate=%d peer=%d connection=%d signing=%d", certificateCalls, peerCalls, connectionCalls, signer.calls)
	}
	if err := gate.wait(ctx); err != nil {
		t.Fatal("handshake leaked admission", err)
	}
}

// TestTLSCertificateSnapshotAndValidation rejects bad key wiring and prevents mutation of retained certificate material.
func TestTLSCertificateSnapshotAndValidation(t *testing.T) {
	certificate, _ := tlsFixtureCertificate(t)
	certificate.OCSPStaple = []byte("ocsp")
	certificate.SignedCertificateTimestamps = [][]byte{[]byte("timestamp")}
	certificate.SupportedSignatureAlgorithms = []tls.SignatureScheme{tls.Ed25519}
	snapshot, err := guardTLSCertificate(certificate, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	originalDER := append([]byte(nil), certificate.Certificate[0]...)
	certificate.Certificate[0][0] ^= 0xff
	certificate.OCSPStaple[0] = 'X'
	certificate.SignedCertificateTimestamps[0][0] = 'X'
	certificate.SupportedSignatureAlgorithms[0] = tls.PSSWithSHA256
	if !bytes.Equal(snapshot.Certificate[0], originalDER) || string(snapshot.OCSPStaple) != "ocsp" || string(snapshot.SignedCertificateTimestamps[0]) != "timestamp" || snapshot.SupportedSignatureAlgorithms[0] != tls.Ed25519 {
		t.Fatal("certificate snapshot aliases caller memory")
	}
	valid, _ := tlsFixtureCertificate(t)
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]tls.Certificate{
		"empty": {}, "bad DER": {Certificate: [][]byte{[]byte("invalid")}},
		"not signer":            {Certificate: valid.Certificate, PrivateKey: struct{}{}},
		"invalid public key":    {Certificate: valid.Certificate, PrivateKey: &tlsFixtureSigner{Signer: valid.PrivateKey.(crypto.Signer), public: struct{}{}}},
		"mismatched public key": {Certificate: valid.Certificate, PrivateKey: &tlsFixtureSigner{Signer: valid.PrivateKey.(crypto.Signer), public: otherPublic}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := guardTLSCertificate(value, newAdmission()); err == nil {
				t.Fatal("invalid TLS material accepted")
			}
		})
	}
	for name, cfg := range map[string]*tls.Config{"unverified": {InsecureSkipVerify: true}, "key logging": {KeyLogWriter: io.Discard}, "invalid certificate": {Certificates: []tls.Certificate{{}}}} {
		t.Run(name, func(t *testing.T) {
			if _, err := guardedTLS(cfg, newAdmission()); err == nil {
				t.Fatal("unsafe TLS configuration accepted")
			}
		})
	}
}

// TestTLSCallbackRejectionAndRedaction proves failures stop the handshake boundary without copying private diagnostics.
func TestTLSCallbackRejectionAndRedaction(t *testing.T) {
	certificate, _ := tlsFixtureCertificate(t)
	for _, mode := range []string{"error", "panic", "nil", "closed", "success"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			calls := 0
			cfg, err := guardedTLS(&tls.Config{GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
				calls++
				if mode == "panic" {
					panic("private callback diagnostic")
				}
				if mode == "error" {
					return nil, errors.New("private callback diagnostic")
				}
				if mode == "nil" {
					return nil, nil
				}
				return &certificate, nil
			}, VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error {
				calls++
				if mode == "panic" {
					panic("private peer diagnostic")
				}
				if mode == "error" {
					return errors.New("private peer diagnostic")
				}
				return nil
			}, VerifyConnection: func(tls.ConnectionState) error {
				calls++
				if mode == "panic" {
					panic("private connection diagnostic")
				}
				if mode == "error" {
					return errors.New("private connection diagnostic")
				}
				return nil
			}}, gate)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "closed" {
				gate.close()
			}
			_, certificateErr := cfg.GetClientCertificate(&tls.CertificateRequestInfo{})
			peerErr := cfg.VerifyPeerCertificate(nil, nil)
			connectionErr := cfg.VerifyConnection(tls.ConnectionState{})
			for _, err := range []error{certificateErr, peerErr, connectionErr} {
				if err != nil && strings.Contains(err.Error(), "private") {
					t.Fatal("TLS diagnostics leaked", err)
				}
			}
			if mode == "success" && (certificateErr != nil || peerErr != nil || connectionErr != nil) {
				t.Fatal("valid callbacks rejected")
			}
			if (mode == "error" || mode == "panic" || mode == "closed") && (certificateErr == nil || peerErr == nil || connectionErr == nil) {
				t.Fatal("TLS failure accepted")
			}
			if mode == "nil" && certificateErr == nil {
				t.Fatal("nil dynamic certificate accepted")
			}
			if mode == "closed" && calls != 0 {
				t.Fatal("disposed TLS dependency invoked")
			}
		})
	}
	calls := 0
	cfg, err := guardedTLS(&tls.Config{VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error { calls++; return nil }}, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	for _, chain := range [][][]byte{make([][]byte, 17), {make([]byte, (1<<20)+1)}} {
		if err := cfg.VerifyPeerCertificate(chain, nil); err == nil {
			t.Fatal("oversized peer chain accepted")
		}
	}
	if calls != 0 {
		t.Fatal("oversized chain reached verifier")
	}
}

// tlsFixtureReader models user-provided entropy that must participate in callback admission.
type tlsFixtureReader struct {
	mode  string
	calls int
}

// Read exposes entropy-source failures without using network timing.
func (r *tlsFixtureReader) Read(buffer []byte) (int, error) {
	r.calls++
	if r.mode == "panic" {
		panic("private entropy diagnostic")
	}
	return copy(buffer, []byte("test")), nil
}

// tlsFixtureCache records session-cache callbacks without retaining network sessions.
type tlsFixtureCache struct {
	mode       string
	gets, puts int
}

// Get models successful and failing cache lookups independently of TLS resumption timing.
func (c *tlsFixtureCache) Get(string) (*tls.ClientSessionState, bool) {
	c.gets++
	if c.mode == "panic" {
		panic("private cache diagnostic")
	}
	return &tls.ClientSessionState{}, true
}

// Put models custom cache writes whose panics must not escape TLS cleanup.
func (c *tlsFixtureCache) Put(string, *tls.ClientSessionState) {
	c.puts++
	if c.mode == "panic" {
		panic("private cache diagnostic")
	}
}

// TestTLSAuxiliaryCallbacksFenceLateWork covers signing, entropy, clock and session dependencies after cleanup admission closes.
func TestTLSAuxiliaryCallbacksFenceLateWork(t *testing.T) {
	certificate, _ := tlsFixtureCertificate(t)
	for _, mode := range []string{"success", "error", "panic", "closed"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			signer := &tlsFixtureSigner{Signer: certificate.PrivateKey.(crypto.Signer), mode: mode}
			guarded := &guardedSigner{inner: signer, public: signer.Public(), gate: gate}
			if mode == "closed" {
				gate.close()
			}
			signature, err := guarded.Sign(rand.Reader, []byte("message"), crypto.Hash(0))
			if mode == "success" {
				if err != nil || !ed25519.Verify(guarded.Public().(ed25519.PublicKey), []byte("message"), signature) {
					t.Fatal("guarded signature failed", err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("unsafe signing result", err)
			}
			if mode == "closed" && signer.calls != 0 {
				t.Fatal("late signer was invoked")
			}
			reader := &tlsFixtureReader{mode: mode}
			cache := &tlsFixtureCache{mode: mode}
			clockCalls := 0
			cfg, err := guardedTLS(&tls.Config{Rand: reader, ClientSessionCache: cache, Time: func() time.Time {
				clockCalls++
				if mode == "panic" {
					panic("private clock diagnostic")
				}
				return time.Unix(42, 0)
			}}, gate)
			if err != nil {
				t.Fatal(err)
			}
			buffer := make([]byte, 4)
			count, readErr := cfg.Rand.Read(buffer)
			state, ok := cfg.ClientSessionCache.Get("peer")
			cfg.ClientSessionCache.Put("peer", state)
			now := cfg.Time()
			if mode == "success" && (readErr != nil || count != 4 || string(buffer) != "test" || !ok || now != time.Unix(42, 0)) {
				t.Fatal("valid TLS auxiliary callback changed")
			}
			if mode == "panic" && (readErr == nil || ok || now == time.Unix(42, 0)) {
				t.Fatal("auxiliary panic was not contained")
			}
			if mode == "closed" && (readErr == nil || ok || reader.calls != 0 || cache.gets != 0 || cache.puts != 0 || clockCalls != 0) {
				t.Fatal("late TLS callback touched dependency")
			}
		})
	}
}

// TestTLSPolicySnapshotPreservesSliceSemantics prevents caller mutation from changing negotiated policy or enabling intentionally disabled defaults.
func TestTLSPolicySnapshotPreservesSliceSemantics(t *testing.T) {
	for _, mode := range []string{"nil", "empty", "populated"} {
		t.Run(mode, func(t *testing.T) {
			source := &tls.Config{}
			if mode == "empty" {
				source.NextProtos = []string{}
				source.CipherSuites = []uint16{}
				source.CurvePreferences = []tls.CurveID{}
				source.EncryptedClientHelloConfigList = []byte{}
			}
			if mode == "populated" {
				source.NextProtos = []string{"h2"}
				source.CipherSuites = []uint16{tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256}
				source.CurvePreferences = []tls.CurveID{tls.X25519}
				source.EncryptedClientHelloConfigList = []byte{1, 2, 3}
			}
			snapshot, err := guardedTLS(source, newAdmission())
			if err != nil {
				t.Fatal(err)
			}
			if (source.NextProtos == nil) != (snapshot.NextProtos == nil) || (source.CipherSuites == nil) != (snapshot.CipherSuites == nil) || (source.CurvePreferences == nil) != (snapshot.CurvePreferences == nil) || (source.EncryptedClientHelloConfigList == nil) != (snapshot.EncryptedClientHelloConfigList == nil) {
				t.Fatal("TLS nil/empty policy distinction changed")
			}
			if mode == "populated" {
				source.NextProtos[0] = "changed"
				source.CipherSuites[0] = tls.TLS_RSA_WITH_AES_128_CBC_SHA
				source.CurvePreferences[0] = tls.CurveP256
				source.EncryptedClientHelloConfigList[0] = 9
				if snapshot.NextProtos[0] != "h2" || snapshot.CipherSuites[0] != tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256 || snapshot.CurvePreferences[0] != tls.X25519 || snapshot.EncryptedClientHelloConfigList[0] != 1 {
					t.Fatal("caller mutation changed TLS policy snapshot")
				}
			}
		})
	}
	certificate, _ := tlsFixtureCertificate(t)
	certificate.SupportedSignatureAlgorithms = []tls.SignatureScheme{}
	snapshot, err := guardedTLS(&tls.Config{Certificates: []tls.Certificate{certificate}}, newAdmission())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Certificates[0].SupportedSignatureAlgorithms == nil {
		t.Fatal("empty certificate signature policy became unrestricted")
	}
	selected, err := snapshot.GetClientCertificate(&tls.CertificateRequestInfo{Version: tls.VersionTLS13, SignatureSchemes: []tls.SignatureScheme{tls.Ed25519}})
	if err != nil || selected == nil || len(selected.Certificate) != 0 {
		t.Fatal("disabled certificate signature policy was ignored", err)
	}
}
