// -------------------------------------------------------------------------------
// API TLS Tests
//
// Author: Alex Freidah
//
// The API over TLS, against certificates minted per test. With a client CA, a
// client whose certificate the CA signed reaches every route, one with no
// certificate reaches only the health check, and one from another CA never
// completes the handshake. Without a client CA, any client reaches every route.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"go.uber.org/mock/gomock"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/state/memory"
	"github.com/afreidah/vagabond/internal/tlsconfig"
	"github.com/afreidah/vagabond/internal/tlsconfig/tlstest"
)

// -------------------------------------------------------------------------
// HARNESS
// -------------------------------------------------------------------------

// serveTLS starts a server over the fixtures on a loopback TLS listener with
// a certificate from ca, verifying client certificates against clientCA when
// it is set, and returns its address.
func serveTLS(t *testing.T, ca, clientCA *tlstest.CA) string {
	t.Helper()

	certFile, keyFile := ca.Server(t)

	var caFile string
	if clientCA != nil {
		caFile = clientCA.File
	}

	cfg, err := tlsconfig.Server(certFile, keyFile, caFile, tls.VerifyClientCertIfGiven)
	if err != nil {
		t.Fatalf("Server() = %v", err)
	}

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	reg, led := fixtures(t)
	srv := New(reg, led, memory.NewExecutions(), NewMockserverJobs(gomock.NewController(t)), slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go func() { done <- srv.ServeListener(ctx, listener, cfg) }()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	return "https://" + listener.Addr().String()
}

// tlsClient is an API client for address trusting ca, presenting a
// certificate from issuer, or none when issuer is nil.
func tlsClient(t *testing.T, address string, ca, issuer *tlstest.CA) *api.Client {
	t.Helper()

	var certFile, keyFile string
	if issuer != nil {
		certFile, keyFile = issuer.Client(t, "ci")
	}

	cfg, err := tlsconfig.Client(ca.File, certFile, keyFile, "")
	if err != nil {
		t.Fatalf("Client() = %v", err)
	}

	client, err := api.NewClient(address, cfg)
	if err != nil {
		t.Fatalf("NewClient() = %v", err)
	}

	return client
}

// -------------------------------------------------------------------------
// TESTS
// -------------------------------------------------------------------------

// A client whose certificate the client CA signed reaches every route.
func TestAPITLS_CertificateReachesEveryRoute(t *testing.T) {
	ca := tlstest.NewCA(t)
	client := tlsClient(t, serveTLS(t, ca, ca), ca, ca)

	if _, err := client.Providers(t.Context()); err != nil {
		t.Errorf("Providers() = %v", err)
	}

	if _, err := client.Health(t.Context()); err != nil {
		t.Errorf("Health() = %v", err)
	}
}

// A client with no certificate is answered 401 everywhere but the health
// check, which a service check asks without one.
func TestAPITLS_NoCertificateReachesOnlyHealth(t *testing.T) {
	ca := tlstest.NewCA(t)
	client := tlsClient(t, serveTLS(t, ca, ca), ca, nil)

	_, err := client.Providers(t.Context())
	if failure, ok := errors.AsType[*api.ResponseError](err); !ok || failure.Status != http.StatusUnauthorized {
		t.Errorf("Providers() = %v, want a 401", err)
	}

	if _, err := client.Health(t.Context()); err != nil {
		t.Errorf("Health() = %v, want it answered without a certificate", err)
	}
}

// A certificate another CA signed fails the handshake, so not even the health
// check answers.
func TestAPITLS_AnotherCAIsRefused(t *testing.T) {
	ca := tlstest.NewCA(t)
	client := tlsClient(t, serveTLS(t, ca, ca), ca, tlstest.NewCA(t))

	for name, call := range map[string]func() error{
		"Providers": func() error { _, err := client.Providers(t.Context()); return err },
		"Health":    func() error { _, err := client.Health(t.Context()); return err },
	} {
		err := call()
		if _, answered := errors.AsType[*api.ResponseError](err); err == nil || answered {
			t.Errorf("%s() = %v, want the handshake refused", name, err)
		}
	}
}

// Without a client CA the API asks for no certificate and serves anyone.
func TestAPITLS_WithoutClientCAServesAnyone(t *testing.T) {
	ca := tlstest.NewCA(t)
	client := tlsClient(t, serveTLS(t, ca, nil), ca, nil)

	if _, err := client.Providers(t.Context()); err != nil {
		t.Errorf("Providers() = %v", err)
	}
}
