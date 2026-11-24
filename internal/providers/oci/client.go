// -------------------------------------------------------------------------------
// OCI Functions Client
//
// Author: Alex Freidah
//
// Signing, and what a refusal from OCI means. Every OCI API takes requests
// signed with the user's API key, which the SDK's signer does; the requests
// themselves go through the shared rest path. The SDK's generated clients are
// not used: two calls do not justify them.
// -------------------------------------------------------------------------------

package oci

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"

	"github.com/afreidah/vagabond/internal/plugin"
	"github.com/afreidah/vagabond/internal/rest"
)

// -------------------------------------------------------------------------
// CONSTANTS
// -------------------------------------------------------------------------

// The Functions API version every path is under, and where a region's API is
// served, its domain filled in from the realm the region belongs to.
const (
	apiVersion         = "20260325"
	managementTemplate = "https://functions.{region}.oci.{secondLevelDomain}"
)

// ErrNoCredential reports an OCI provider configured without a private key.
var ErrNoCredential = errors.New("no credential supplied")

// functionFaults are the codes OCI answers an invoke with when the function
// itself failed: it crashed, replied wrongly or too much, or ran past its
// timeout. The run is over, so each is the task's answer, not a failed call.
var functionFaults = []string{
	"FunctionInvokeExecutionFailed",
	"FunctionInvokeExecutionError",
	"FunctionInvokeInvalidResponse",
	"FunctionInvokeResponseBodyTooLarge",
	"FunctionInvokeResponseHeaderTooLarge",
	"FunctionInvokeTimeout",
}

// -------------------------------------------------------------------------
// SIGNING
// -------------------------------------------------------------------------

// newHTTPClient builds a client that signs every request with key, the private
// half of the API key cfg names. A key that cannot be parsed fails at startup,
// not at the first dispatch.
func newHTTPClient(cfg *Config, key []byte) (*http.Client, error) {
	if len(key) == 0 {
		return nil, ErrNoCredential
	}

	provider := common.NewRawConfigurationProvider(cfg.Tenancy, cfg.User, cfg.Region, cfg.Fingerprint, string(key), nil)
	if _, err := provider.PrivateRSAKey(); err != nil {
		return nil, fmt.Errorf("parsing the private key: %w", err)
	}

	return &http.Client{Transport: signer{sign: common.DefaultRequestSigner(provider), next: http.DefaultTransport}}, nil
}

// signer is a transport signing each request before next sends it.
type signer struct {
	sign common.HTTPRequestSigner
	next http.RoundTripper
}

// RoundTrip dates and signs a copy of the request, since a transport must not
// change the caller's, and sends it. The date is part of the signature.
func (s signer) RoundTrip(req *http.Request) (*http.Response, error) {
	signed := req.Clone(req.Context())
	signed.Header.Set("Date", time.Now().UTC().Format(http.TimeFormat))

	if err := s.sign.Sign(signed); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}

		return nil, fmt.Errorf("signing the request: %w", err)
	}

	return s.next.RoundTrip(signed)
}

// managementEndpoint is where region's Functions API is served, in whichever
// realm the region belongs to.
func managementEndpoint(region string) string {
	return common.StringToRegion(region).EndpointForTemplate("functions", managementTemplate)
}

// -------------------------------------------------------------------------
// REFUSALS
// -------------------------------------------------------------------------

// functionFailed reports whether a refusal is the function itself failing,
// which ends the run with an answer rather than failing the call.
func functionFailed(r *rest.Refusal) bool {
	code, _ := ociFault(r.Body)

	return slices.Contains(functionFaults, code)
}

// refused classifies any other non-2xx by its status, carrying OCI's own code
// and message from the body.
func refused(r *rest.Refusal) error {
	return plugin.ClassifyHTTP(r.Status, r.RetryAfter(), fmt.Errorf("%s: %s", r.Method, ociError(r.Body)))
}

// ociError renders what OCI said went wrong, or the body as received when it
// is not OCI's error.
func ociError(body []byte) string {
	if code, message := ociFault(body); message != "" {
		return code + ": " + message
	}

	return rest.Excerpt(body)
}

// ociFault reads the code and message of OCI's error body, both empty when the
// body is not one.
func ociFault(body []byte) (code, message string) {
	var out struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}

	_ = json.Unmarshal(body, &out)

	return out.Code, out.Message
}
