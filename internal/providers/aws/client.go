// -------------------------------------------------------------------------------
// Lambda Client
//
// Author: Alex Freidah
//
// The SDK client, its credential, and the classification of what it returns.
// The SDK's own retries are switched off: Vagabond owns retry, with the ledger
// watching, and an SDK retrying a throttled invoke would run the work twice
// behind its back.
// -------------------------------------------------------------------------------

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	sdkconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/afreidah/vagabond/internal/plugin"
)

// processCredential is the credential_process output format, which is what
// `aws configure export-credentials --format process` prints. The expiry is
// ignored: a CLI process outlives no session token worth refreshing.
type processCredential struct {
	Version         int
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string
	SessionToken    string
}

// newClient builds a Lambda client for the configured region.
//
// No credential uses the SDK's default chain: environment, shared config and
// SSO, AWS_PROFILE included. A credential is read as credential_process JSON,
// so a credentials block running `aws configure export-credentials` works.
func newClient(ctx context.Context, cfg *Config, credential []byte) (*lambda.Client, error) {
	opts := []func(*sdkconfig.LoadOptions) error{
		sdkconfig.WithRegion(cfg.Region),
		sdkconfig.WithRetryer(func() sdkaws.Retryer { return sdkaws.NopRetryer{} }),
	}

	if len(credential) > 0 {
		provider, err := staticCredential(credential)
		if err != nil {
			return nil, err
		}

		opts = append(opts, sdkconfig.WithCredentialsProvider(provider))
	}

	loaded, err := sdkconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("loading AWS configuration: %w", err)
	}

	return lambda.NewFromConfig(loaded), nil
}

// staticCredential parses credential_process JSON.
func staticCredential(raw []byte) (credentials.StaticCredentialsProvider, error) {
	var c processCredential

	if err := json.Unmarshal(raw, &c); err != nil {
		return credentials.StaticCredentialsProvider{}, fmt.Errorf("parsing the credential: %w", err)
	}

	if c.Version != 1 || c.AccessKeyID == "" || c.SecretAccessKey == "" {
		return credentials.StaticCredentialsProvider{}, errors.New(
			"the credential is not credential_process JSON with Version 1, AccessKeyId and SecretAccessKey")
	}

	return credentials.NewStaticCredentialsProvider(c.AccessKeyID, c.SecretAccessKey, c.SessionToken), nil
}

// classify turns an SDK error into a classified failure. An error with an HTTP
// response goes through the shared status mapping; one without never reached
// AWS, so it is infrastructure.
func classify(err error) error {
	var resp *awshttp.ResponseError
	if errors.As(err, &resp) {
		return plugin.ClassifyHTTP(resp.HTTPStatusCode(), 0, err)
	}

	return plugin.Infrastructure(err)
}
