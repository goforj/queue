package temporalqueue

import (
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/interceptor"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/stats"
)

// credentialSystemInfoService observes connection-level authentication without replacing SDK client initialization.
type credentialSystemInfoService struct {
	workflowservice.UnimplementedWorkflowServiceServer
	requests chan []string
	closed   chan struct{}
}

// GetSystemInfo provides actual SDK capability negotiation and records the authenticated transport metadata.
func (s *credentialSystemInfoService) GetSystemInfo(ctx context.Context, _ *workflowservice.GetSystemInfoRequest) (*workflowservice.GetSystemInfoResponse, error) {
	headers, _ := metadata.FromIncomingContext(ctx)
	s.requests <- []string{strings.Join(headers.Get("authorization"), ","), strings.Join(headers.Get("fixture-scope"), ",")}
	return &workflowservice.GetSystemInfoResponse{Capabilities: &workflowservice.GetSystemInfoResponse_Capabilities{}}, nil
}

// credentialTLSServer hosts only the real SDK initialization RPC, with verified optional or mandatory client authentication.
func credentialTLSServer(t *testing.T, certificate tls.Certificate, root *x509.Certificate, policy tls.ClientAuthType) (string, *credentialSystemInfoService) {
	t.Helper()
	trust := x509.NewCertPool()
	trust.AddCert(root)
	material := certificate
	material.Certificate = make([][]byte, len(certificate.Certificate))
	for i, raw := range certificate.Certificate {
		material.Certificate[i] = append([]byte(nil), raw...)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	service := &credentialSystemInfoService{requests: make(chan []string, 8), closed: make(chan struct{}, 8)}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{material}, ClientAuth: policy, ClientCAs: trust, MinVersion: tls.VersionTLS13})), grpc.StatsHandler(service))
	workflowservice.RegisterWorkflowServiceServer(server, service)
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-done; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Error(err)
		}
	})
	return listener.Addr().String(), service
}

// TestSDKMTLSCredentialsAreSnapshottedAndSharedWithoutReapplication covers SDK-added certificates and actual shared-client negotiation.
func TestSDKMTLSCredentialsAreSnapshottedAndSharedWithoutReapplication(t *testing.T) {
	certificate, root := tlsFixtureCertificate(t)
	address, _ := credentialTLSServer(t, certificate, root, tls.RequireAndVerifyClientCert)
	signer := &tlsFixtureSigner{Signer: certificate.PrivateKey.(crypto.Signer)}
	certificate.PrivateKey = signer
	trust := x509.NewCertPool()
	trust.AddCert(root)
	binding, err := NewClientBinding(client.Options{HostPort: address, Credentials: client.NewMTLSCredentials(certificate), ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{RootCAs: trust, ServerName: "localhost"}}}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := binding.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	certificate.Certificate[0][0] ^= 0xff
	b := &backend{clientBinding: binding, entry: newAdmission(), gate: newAdmission()}
	options, err := b.workerClientOptions()
	if err != nil {
		t.Fatal(err)
	}
	if options.Credentials != nil {
		t.Fatal("shared connection credentials would be reapplied")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	derivative, err := client.NewClientFromExistingWithContext(ctx, binding.Client(), options)
	if err != nil {
		t.Fatal("SDK mTLS worker client initialization failed", err)
	}
	defer derivative.Close()
	if _, err := derivative.WorkflowService().GetSystemInfo(ctx, &workflowservice.GetSystemInfoRequest{}); err != nil {
		t.Fatal("derived client lost authenticated connection", err)
	}
	if signer.calls != 1 {
		t.Fatal("client certificate was not selected through guarded signing", signer.calls)
	}
}

// TestSharedWorkerClientPreservesDynamicConnectionAuthentication proves clearing derivative credentials retains API-key rotation and request headers.
func TestSharedWorkerClientPreservesDynamicConnectionAuthentication(t *testing.T) {
	certificate, root := tlsFixtureCertificate(t)
	address, service := credentialTLSServer(t, certificate, root, tls.VerifyClientCertIfGiven)
	trust := x509.NewCertPool()
	trust.AddCert(root)
	var token atomic.Value
	token.Store("synthetic-first")
	binding, err := NewClientBinding(client.Options{HostPort: address, Credentials: client.NewAPIKeyDynamicCredentials(func(context.Context) (string, error) { return token.Load().(string), nil }), HeadersProvider: &requestHeadersFixture{headers: map[string]string{"fixture-scope": "reports"}}, ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{RootCAs: trust, ServerName: "localhost", GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return new(tls.Certificate), nil }}}}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := binding.Close(t.Context()); err != nil {
			t.Error(err)
		}
	})
	b := &backend{clientBinding: binding, entry: newAdmission(), gate: newAdmission()}
	options, err := b.workerClientOptions()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	derivative, err := client.NewClientFromExistingWithContext(ctx, binding.Client(), options)
	if err != nil {
		t.Fatal(err)
	}
	defer derivative.Close()
	select {
	case headers := <-service.requests:
		if headers[0] != "Bearer synthetic-first" || headers[1] != "reports" {
			t.Fatal("initial authentication changed", headers)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	token.Store("synthetic-second")
	if _, err := derivative.WorkflowService().GetSystemInfo(ctx, &workflowservice.GetSystemInfoRequest{}); err != nil {
		t.Fatal(err)
	}
	select {
	case headers := <-service.requests:
		if headers[0] != "Bearer synthetic-second" || headers[1] != "reports" {
			t.Fatal("derived client lost rotation or headers", headers)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

// retainedCredentialSigner models a private-key provider whose actual return must precede binding cleanup.
type retainedCredentialSigner struct {
	crypto.Signer
	entered, release chan struct{}
}

// Sign retains application key-service lifetime after the caller's close context expires.
func (s *retainedCredentialSigner) Sign(random io.Reader, digest []byte, options crypto.SignerOpts) ([]byte, error) {
	close(s.entered)
	<-s.release
	return s.Signer.Sign(random, digest, options)
}

// TestSDKMTLSSigningRetainsBindingDuringClose verifies SDK-added key providers receive the same ownership fence as explicit TLS certificates.
func TestSDKMTLSSigningRetainsBindingDuringClose(t *testing.T) {
	certificate, _ := tlsFixtureCertificate(t)
	signer := &retainedCredentialSigner{Signer: certificate.PrivateKey.(crypto.Signer), entered: make(chan struct{}), release: make(chan struct{})}
	certificate.PrivateKey = signer
	binding, err := NewClientBinding(client.Options{Credentials: client.NewMTLSCredentials(certificate), ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{}}}, SerializationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := binding.options.ConnectionOptions.TLS.GetClientCertificate(&tls.CertificateRequestInfo{Version: tls.VersionTLS13, SignatureSchemes: []tls.SignatureScheme{tls.Ed25519}})
	if err != nil || len(selected.Certificate) == 0 {
		t.Fatal("SDK certificate selection failed", err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := selected.PrivateKey.(crypto.Signer).Sign(nil, []byte("fixture"), crypto.Hash(0))
		result <- err
	}()
	<-signer.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = binding.Close(ctx)
	close(signer.release)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("binding closed across active SDK credential signer", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// TestTLSDefaultSelectionDeclinesUnsupportedRequests preserves optional client-authentication semantics.
func TestTLSDefaultSelectionDeclinesUnsupportedRequests(t *testing.T) {
	certificate, _ := tlsFixtureCertificate(t)
	gate := newAdmission()
	cfg, err := guardedTLS(&tls.Config{Certificates: []tls.Certificate{certificate}}, gate)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []*tls.CertificateRequestInfo{{Version: tls.VersionTLS13, SignatureSchemes: []tls.SignatureScheme{tls.PSSWithSHA256}}, {Version: tls.VersionTLS13, SignatureSchemes: []tls.SignatureScheme{tls.Ed25519}, AcceptableCAs: [][]byte{[]byte("unrelated issuer")}}} {
		selected, err := cfg.GetClientCertificate(request)
		if err != nil || selected == nil || len(selected.Certificate) != 0 {
			t.Fatal("optional client auth did not decline unsupported certificate", err)
		}
	}
	gate.close()
	if _, err := cfg.GetClientCertificate(&tls.CertificateRequestInfo{}); !errors.Is(err, errAdmissionClosed) {
		t.Fatal("late certificate selection escaped admission", err)
	}
}

// TestECHVerifierFailureAndDrain tracks the verifier Go uses instead of normal peer verification on ECH rejection.
func TestECHVerifierFailureAndDrain(t *testing.T) {
	for _, mode := range []string{"success", "error", "panic", "closed"} {
		t.Run(mode, func(t *testing.T) {
			gate := newAdmission()
			calls := 0
			cfg, err := guardedTLS(&tls.Config{EncryptedClientHelloRejectionVerify: func(tls.ConnectionState) error {
				calls++
				if mode == "error" {
					return errors.New("private ECH diagnostic")
				}
				if mode == "panic" {
					panic("private ECH diagnostic")
				}
				return nil
			}}, gate)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "closed" {
				gate.close()
			}
			err = cfg.EncryptedClientHelloRejectionVerify(tls.ConnectionState{})
			if mode == "success" {
				if err != nil || calls != 1 {
					t.Fatal(err)
				}
			} else if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("ECH failure escaped safe boundary", err)
			}
			if mode == "closed" && calls != 0 {
				t.Fatal("late ECH callback touched verifier")
			}
		})
	}
	gate := newAdmission()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	cfg, err := guardedTLS(&tls.Config{EncryptedClientHelloRejectionVerify: func(tls.ConnectionState) error { close(entered); <-release; return nil }}, gate)
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- cfg.EncryptedClientHelloRejectionVerify(tls.ConnectionState{}) }()
	<-entered
	gate.close()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err = gate.wait(ctx)
	close(release)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("ECH verifier escaped drain", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := gate.wait(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// captureCredentialClientFactory retains the initialized SDK chain only to verify failed constructor cleanup.
type captureCredentialClientFactory struct {
	interceptor.ClientInterceptorBase
	next          interceptor.ClientOutboundInterceptor
	initialize    bool
	initializeErr error
}

// InterceptClient records the SDK-owned connection before post-construction certificate normalization.
func (c *captureCredentialClientFactory) InterceptClient(next interceptor.ClientOutboundInterceptor) interceptor.ClientOutboundInterceptor {
	c.next = next
	if c.initialize {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, c.initializeErr = next.DescribeWorkflow(ctx, &interceptor.ClientDescribeWorkflowInput{WorkflowID: "construction-probe"})
	}
	return next
}

// panicPublicSignerFixture exposes a provider failure that occurs only after SDK credentials append their certificate.
type panicPublicSignerFixture struct{ crypto.Signer }

// Public isolates configuration-time provider failure from genuine application activity panics.
func (*panicPublicSignerFixture) Public() crypto.PublicKey {
	panic("private public-key provider diagnostic")
}

// TestSDKCredentialSnapshotFailureClosesCreatedClient covers rollback after SDK factories already obtained the lazy connection.
func TestSDKCredentialSnapshotFailureClosesCreatedClient(t *testing.T) {
	for _, mode := range []string{"malformed certificate", "provider panic"} {
		t.Run(mode, func(t *testing.T) {
			certificate, root := tlsFixtureCertificate(t)
			address, service := credentialTLSServer(t, certificate, root, tls.NoClientCert)
			if mode == "malformed certificate" {
				certificate.Certificate = [][]byte{[]byte("invalid certificate")}
			} else {
				certificate.PrivateKey = &panicPublicSignerFixture{Signer: certificate.PrivateKey.(crypto.Signer)}
			}
			trust := x509.NewCertPool()
			trust.AddCert(root)
			factory := &captureCredentialClientFactory{initialize: true}
			binding, err := NewClientBinding(client.Options{HostPort: address, Credentials: client.NewMTLSCredentials(certificate), Interceptors: []interceptor.ClientInterceptor{factory}, ConnectionOptions: client.ConnectionOptions{TLS: &tls.Config{RootCAs: trust, ServerName: "localhost"}}}, SerializationLimits{})
			if err == nil || binding != nil || strings.Contains(err.Error(), "private") || factory.next == nil {
				t.Fatal("post-SDK credential failure not rejected safely", binding, err)
			}

			if factory.initializeErr != nil {
				t.Fatal("rollback fixture never initialized its SDK connection", factory.initializeErr)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			select {
			case <-service.closed:
			case <-ctx.Done():
				t.Fatal("failed constructor did not close initialized SDK connection", ctx.Err())
			}
			_, err = factory.next.DescribeWorkflow(ctx, &interceptor.ClientDescribeWorkflowInput{WorkflowID: "after-rollback"})
			if err == nil {
				t.Fatal("failed constructor retained an active SDK chain")
			}

		})
	}
}

// TestTLSSelectionBeforeCredentialFinalizationIsGuarded covers factory-initiated handshakes before NewLazyClient returns.
func TestTLSSelectionBeforeCredentialFinalizationIsGuarded(t *testing.T) {
	gate := newAdmission()
	policy, err := newTLSPolicy(&tls.Config{}, gate)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := tlsFixtureCertificate(t)
	policy.configuration.Certificates = append(policy.configuration.Certificates, certificate)
	request := &tls.CertificateRequestInfo{Version: tls.VersionTLS13, SignatureSchemes: []tls.SignatureScheme{tls.Ed25519}}
	selected, err := policy.configuration.GetClientCertificate(request)
	if err != nil {
		t.Fatal(err)
	}
	signer, ok := selected.PrivateKey.(*guardedSigner)
	if !ok || signer.gate != gate {
		t.Fatal("early SDK certificate selection exposed an unguarded signer")
	}
	if err := policy.snapshotCertificates(); err != nil {
		t.Fatal(err)
	}
	certificate.Certificate[0][0] ^= 0xff
	if _, err := x509.ParseCertificate(selected.Certificate[0]); err != nil {
		t.Fatal("early selection retained caller bytes", err)
	}
	var joined sync.WaitGroup
	for range 16 {
		joined.Go(func() {
			if err := policy.snapshotCertificates(); err != nil {
				t.Error(err)
			}
		})
		joined.Go(func() {
			if _, err := policy.configuration.GetClientCertificate(request); err != nil {
				t.Error(err)
			}
		})
	}
	joined.Wait()
}

// DescribeWorkflowExecution permits a factory to initialize its connection before certificate finalization fails.
func (s *credentialSystemInfoService) DescribeWorkflowExecution(context.Context, *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{}, nil
}

// TagRPC leaves SDK metadata intact while transport cleanup is observed independently.
func (s *credentialSystemInfoService) TagRPC(ctx context.Context, _ *stats.RPCTagInfo) context.Context {
	return ctx
}

// HandleRPC avoids adding lifecycle work to the fixture's application RPC path.
func (s *credentialSystemInfoService) HandleRPC(context.Context, stats.RPCStats) {}

// TagConn preserves the transport context for actual connection-end observation.
func (s *credentialSystemInfoService) TagConn(ctx context.Context, _ *stats.ConnTagInfo) context.Context {
	return ctx
}

// HandleConn records physical connection closure even when admission fences prevent further SDK calls.
func (s *credentialSystemInfoService) HandleConn(_ context.Context, event stats.ConnStats) {
	if _, closed := event.(*stats.ConnEnd); closed {
		s.closed <- struct{}{}
	}
}
