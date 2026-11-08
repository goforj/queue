package temporalqueue

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"time"
)

// guardedTLS snapshots public trust material and fences callbacks that gRPC may run after RPC cancellation.
func guardedTLS(configuration *tls.Config, gate *admission) (*tls.Config, error) {
	snapshot := configuration.Clone()
	if snapshot.InsecureSkipVerify || snapshot.KeyLogWriter != nil {
		return nil, errors.New("Temporal TLS requires verified transport without key logging")
	}
	if snapshot.RootCAs != nil {
		snapshot.RootCAs = snapshot.RootCAs.Clone()
	}
	if snapshot.ClientCAs != nil {
		snapshot.ClientCAs = snapshot.ClientCAs.Clone()
	}
	snapshot.Certificates = append([]tls.Certificate(nil), configuration.Certificates...)
	for i, certificate := range configuration.Certificates {
		copy, err := guardTLSCertificate(certificate, gate)
		if err != nil {
			return nil, err
		}
		snapshot.Certificates[i] = *copy
	}
	if callback := configuration.GetClientCertificate; callback != nil {
		snapshot.GetClientCertificate = func(info *tls.CertificateRequestInfo) (result *tls.Certificate, err error) {
			defer tlsFailure(&err)
			release, err := gate.enter()
			if err != nil {
				return nil, err
			}
			defer release()
			certificate, err := callback(info)
			if err != nil {
				return nil, errors.New("Temporal TLS certificate provider failed")
			}
			if certificate == nil {
				return nil, errors.New("Temporal TLS certificate provider returned no certificate")
			}
			return guardTLSCertificate(*certificate, gate)
		}
	}
	if callback := configuration.VerifyPeerCertificate; callback != nil {
		snapshot.VerifyPeerCertificate = func(raw [][]byte, verified [][]*x509.Certificate) (err error) {
			defer tlsFailure(&err)
			release, err := gate.enter()
			if err != nil {
				return err
			}
			defer release()
			if len(raw) > 16 {
				return errors.New("Temporal TLS peer chain exceeds limit")
			}
			total := 0
			for _, certificate := range raw {
				total += len(certificate)
				if total > 1<<20 {
					return errors.New("Temporal TLS peer chain exceeds limit")
				}
			}
			if err := callback(raw, verified); err != nil {
				return errors.New("Temporal TLS peer verification failed")
			}
			return nil
		}
	}
	if callback := configuration.VerifyConnection; callback != nil {
		snapshot.VerifyConnection = func(state tls.ConnectionState) (err error) {
			defer tlsFailure(&err)
			release, err := gate.enter()
			if err != nil {
				return err
			}
			defer release()
			if err := callback(state); err != nil {
				return errors.New("Temporal TLS connection verification failed")
			}
			return nil
		}
	}
	if callback := configuration.Time; callback != nil {
		snapshot.Time = func() time.Time { return adapterCall(gate, time.Now(), callback) }
	}
	if configuration.Rand != nil {
		snapshot.Rand = &admittedReader{inner: configuration.Rand, gate: gate}
	}
	if configuration.ClientSessionCache != nil {
		snapshot.ClientSessionCache = &guardedSessionCache{inner: configuration.ClientSessionCache, gate: gate}
	}
	return snapshot, nil
}

// tlsFailure removes callback-controlled panic text from TLS transport diagnostics.
func tlsFailure(err *error) {
	if recover() != nil {
		*err = errors.New("Temporal TLS adapter failed")
	}
}

// guardTLSCertificate copies certificate bytes and tracks dynamic private-key signing callbacks.
func guardTLSCertificate(certificate tls.Certificate, gate *admission) (*tls.Certificate, error) {
	if len(certificate.Certificate) == 0 {
		return nil, errors.New("Temporal TLS certificate chain is empty")
	}
	copy := certificate
	copy.Certificate = make([][]byte, len(certificate.Certificate))
	for i, raw := range certificate.Certificate {
		copy.Certificate[i] = append([]byte(nil), raw...)
	}
	leaf, err := x509.ParseCertificate(copy.Certificate[0])
	if err != nil {
		return nil, errors.New("Temporal TLS certificate is invalid")
	}
	copy.Leaf = leaf
	signer, ok := certificate.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, errors.New("Temporal TLS certificate requires a signing key")
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return nil, errors.New("Temporal TLS signing key is invalid")
	}
	clonedPublic, err := x509.ParsePKIXPublicKey(public)
	if err != nil {
		return nil, errors.New("Temporal TLS signing key is invalid")
	}
	expected, err := x509.MarshalPKIXPublicKey(leaf.PublicKey)
	if err != nil || !bytes.Equal(public, expected) {
		return nil, errors.New("Temporal TLS certificate and signing key do not match")
	}
	copy.PrivateKey = &guardedSigner{inner: signer, public: clonedPublic, gate: gate}
	copy.OCSPStaple = append([]byte(nil), certificate.OCSPStaple...)
	copy.SignedCertificateTimestamps = make([][]byte, len(certificate.SignedCertificateTimestamps))
	for i, value := range certificate.SignedCertificateTimestamps {
		copy.SignedCertificateTimestamps[i] = append([]byte(nil), value...)
	}
	copy.SupportedSignatureAlgorithms = append([]tls.SignatureScheme(nil), certificate.SupportedSignatureAlgorithms...)
	return &copy, nil
}

// guardedSigner retains private-key provider dependencies through actual handshake signing.
type guardedSigner struct {
	inner  crypto.Signer
	public crypto.PublicKey
	gate   *admission
}

// Public returns the immutable public material captured during configuration validation.
func (s *guardedSigner) Public() crypto.PublicKey { return s.public }

// Sign retains HSM/key-service ownership until the actual signing callback returns.
func (s *guardedSigner) Sign(random io.Reader, digest []byte, options crypto.SignerOpts) (result []byte, err error) {
	defer tlsFailure(&err)
	release, err := s.gate.enter()
	if err != nil {
		return nil, err
	}
	defer release()
	result, err = s.inner.Sign(random, digest, options)
	if err != nil {
		return nil, errors.New("Temporal TLS signing failed")
	}
	return result, nil
}

// admittedReader fences custom handshake entropy sources that ignore RPC cancellation.
type admittedReader struct {
	inner io.Reader
	gate  *admission
}

// Read retains the source dependency until its actual callback returns.
func (r *admittedReader) Read(buffer []byte) (count int, err error) {
	defer tlsFailure(&err)
	release, err := r.gate.enter()
	if err != nil {
		return 0, err
	}
	defer release()
	return r.inner.Read(buffer)
}

// guardedSessionCache joins user cache callbacks invoked by background TLS handshakes.
type guardedSessionCache struct {
	inner tls.ClientSessionCache
	gate  *admission
}

// Get suppresses unavailable late cache lookups without touching disposed dependencies.
func (c *guardedSessionCache) Get(key string) (*tls.ClientSessionState, bool) {
	type result struct {
		state *tls.ClientSessionState
		ok    bool
	}
	value := adapterCall(c.gate, result{}, func() result { state, ok := c.inner.Get(key); return result{state, ok} })
	return value.state, value.ok
}

// Put suppresses late cache updates while joining admitted callbacks.
func (c *guardedSessionCache) Put(key string, value *tls.ClientSessionState) {
	adapterCall(c.gate, false, func() bool { c.inner.Put(key, value); return true })
}
