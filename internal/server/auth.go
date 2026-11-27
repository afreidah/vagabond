// -------------------------------------------------------------------------------
// API Authentication
//
// Author: Alex Freidah
//
// Who may call the API. With client CAs configured, every request but health
// presents a verified certificate. With tokens configured, every request but
// health carries one, and each route says what it needs: any token, read or
// write in the namespace the request acts in, or a management token.
// -------------------------------------------------------------------------------

package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/afreidah/vagabond/internal/api"
	"github.com/afreidah/vagabond/internal/logging"
	"github.com/afreidah/vagabond/internal/tlsconfig"
)

// -------------------------------------------------------------------------
// TYPES
// -------------------------------------------------------------------------

// healthPath is the one route a client may ask without a certificate or a
// token, since a load balancer or service check probes it.
const healthPath = "/v1/health"

// Access is what a route needs. Authenticated takes any token; Read and Write
// take that policy, or write for read, in the namespace the request acts in;
// Management takes a management token.
type Access int

const (
	Authenticated Access = iota
	Read
	Write
	Management
)

// String names the access, as a refusal shows it.
func (a Access) String() string {
	return [...]string{"any", "read", "write", "management"}[a]
}

// Token is an API token the server accepts: the name logs and records show for
// its requests, its secret, and its access in each namespace, Read or Write. A
// management token has every access everywhere.
type Token struct {
	Name       string
	Secret     string
	Management bool
	Namespaces map[string]Access
}

// token is a Token with the SHA-256 digest of its secret, which a request's
// secret is compared against.
type token struct {
	Token
	digest [sha256.Size]byte
}

// grant is the token a request authenticated with and the access its route
// needs, kept on the request's context for permit.
type grant struct {
	token *token
	need  Access
}

// grantKey is the context key a grant is kept under.
type grantKey struct{}

// -------------------------------------------------------------------------
// TOKENS
// -------------------------------------------------------------------------

// route serves h to requests holding need, checked by authenticate and
// encoded by wrap.
func (s *Server) route(need Access, h handler) http.HandlerFunc {
	return s.authenticate(need, s.wrap(h))
}

// authenticate answers 401 to a request carrying none of the server's tokens,
// and 403 to one without the management token its route needs. The rest go on
// with their grant, and each line they log names the token. Without tokens
// configured, every request goes on.
func (s *Server) authenticate(need Access, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(s.tokens) == 0 {
			next(w, r)

			return
		}

		secret := presented(r)
		t := s.match(secret)

		switch {
		case t == nil:
			reason := "this server requires an API token"
			if secret != "" {
				reason = "this server does not accept the API token sent"
			}

			w.Header().Set("WWW-Authenticate", "Bearer")
			write(w, http.StatusUnauthorized, api.Error{Error: reason})

		case need == Management && !t.Management:
			write(w, http.StatusForbidden, api.Error{Error: fmt.Sprintf("token %q is not a management token", t.Name)})

		default:
			ctx := context.WithValue(r.Context(), grantKey{}, grant{token: t, need: need})

			next(w, r.WithContext(logging.With(ctx, "token", t.Name)))
		}
	}
}

// permit refuses with a 403 a request whose token lacks its route's access in
// namespace. Without tokens configured, every request is permitted.
func (s *Server) permit(r *http.Request, namespace string) error {
	if len(s.tokens) == 0 {
		return nil
	}

	g, ok := r.Context().Value(grantKey{}).(grant)
	if !ok {
		return refuse(http.StatusForbidden, errors.New("the request was not authenticated"))
	}

	if g.token.Management || g.token.Namespaces[namespace] >= g.need {
		return nil
	}

	return refuse(http.StatusForbidden, fmt.Errorf("token %q has no %s access to namespace %q",
		g.token.Name, g.need, namespace))
}

// match returns the token whose secret is secret, or nil. Every digest is
// compared in constant time, so how long it takes says nothing about which
// token matched or how much of one did.
func (s *Server) match(secret string) *token {
	digest := sha256.Sum256([]byte(secret))

	var found *token

	for i := range s.tokens {
		if subtle.ConstantTimeCompare(digest[:], s.tokens[i].digest[:]) == 1 {
			found = &s.tokens[i]
		}
	}

	return found
}

// presented is the secret a request carries in X-Vagabond-Token, or else as a
// bearer token in Authorization.
func presented(r *http.Request) string {
	if secret := r.Header.Get(api.TokenHeader); secret != "" {
		return secret
	}

	scheme, secret, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	if strings.EqualFold(scheme, "Bearer") {
		return secret
	}

	return ""
}

// caller names who sent a request: its token, or without tokens configured,
// the common name on its client certificate, empty when it presented none.
func caller(r *http.Request) string {
	if g, ok := r.Context().Value(grantKey{}).(grant); ok {
		return g.token.Name
	}

	return tlsconfig.PeerName(r.TLS)
}

// -------------------------------------------------------------------------
// CLIENT CERTIFICATES
// -------------------------------------------------------------------------

// requireCertificate answers 401 to a request that presented no verified
// client certificate, health checks aside. The listener verifies a certificate
// only when one is given, so the health check can still connect without one.
func requireCertificate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != healthPath && (r.TLS == nil || len(r.TLS.VerifiedChains) == 0) {
			write(w, http.StatusUnauthorized, api.Error{Error: "this server requires a client certificate"})

			return
		}

		next.ServeHTTP(w, r)
	})
}
