package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/config"
)

const cognitoDeleteTimeout = 10 * time.Second
const cognitoDeleteResponseLimit = 64 << 10

var (
	cognitoDeleteRegionPattern = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+$`)
	cognitoDeletePoolPattern   = regexp.MustCompile(`^[a-z]{2}(?:-[a-z0-9]+)+-[0-9]+_[0-9a-zA-Z]+$`)
	errCognitoDelete           = errors.New("Cognito account deletion failed")
)

type cognitoDeleteOptions struct {
	credentials aws.CredentialsProvider
	client      *http.Client
}

type CognitoDeleteOption func(*cognitoDeleteOptions)

// WithCognitoDeleteCredentials injects a credential provider, primarily for
// isolated tests. Normal deployments use the default AWS credential chain.
func WithCognitoDeleteCredentials(provider aws.CredentialsProvider) CognitoDeleteOption {
	return func(options *cognitoDeleteOptions) { options.credentials = provider }
}

// WithCognitoDeleteHTTPClient supplies a transport without permitting endpoint
// overrides. The constructor always enforces the timeout and redirect policy.
func WithCognitoDeleteHTTPClient(client *http.Client) CognitoDeleteOption {
	return func(options *cognitoDeleteOptions) { options.client = client }
}

type awsCognitoDeleter struct {
	region      string
	poolID      string
	endpoint    string
	credentials aws.CredentialsProvider
	client      *http.Client
	now         func() time.Time
}

// NewAWSCognitoDeleter implements the documented AdminDeleteUser JSON protocol
// using the existing AWS SDK's SigV4 signer and credential providers.
func NewAWSCognitoDeleter(ctx context.Context, region, poolID string, options ...CognitoDeleteOption) (CognitoUserDeleter, error) {
	if !cognitoDeleteRegionPattern.MatchString(region) ||
		!strings.HasPrefix(poolID, region+"_") || !cognitoDeletePoolPattern.MatchString(poolID) ||
		len(poolID) <= len(region)+1 || len(poolID) > 55 {
		return nil, errors.New("valid Cognito region and user pool configuration are required")
	}
	settings := cognitoDeleteOptions{}
	for _, option := range options {
		if option != nil {
			option(&settings)
		}
	}
	client := &http.Client{}
	if settings.client != nil {
		*client = *settings.client
	}
	if client.Timeout <= 0 || client.Timeout > cognitoDeleteTimeout {
		client.Timeout = cognitoDeleteTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	provider := settings.credentials
	if provider == nil {
		cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region), config.WithHTTPClient(client))
		if err != nil {
			return nil, errors.New("AWS credentials could not be configured")
		}
		provider = cfg.Credentials
	}
	if provider == nil {
		return nil, errors.New("AWS credentials are required")
	}
	return &awsCognitoDeleter{
		region: region, poolID: poolID, endpoint: "https://cognito-idp." + region + ".amazonaws.com/",
		credentials: provider, client: client, now: time.Now,
	}, nil
}

func (deleter *awsCognitoDeleter) DeleteUser(ctx context.Context, username string) error {
	if strings.TrimSpace(username) == "" {
		return errCognitoDelete
	}
	ctx, cancel := context.WithTimeout(ctx, cognitoDeleteTimeout)
	defer cancel()
	payload, err := json.Marshal(struct {
		UserPoolID string `json:"UserPoolId"`
		Username   string `json:"Username"`
	}{deleter.poolID, username})
	if err != nil {
		return errCognitoDelete
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, deleter.endpoint, bytes.NewReader(payload))
	if err != nil {
		return errCognitoDelete
	}
	request.Header.Set("Content-Type", "application/x-amz-json-1.1")
	request.Header.Set("X-Amz-Target", "AWSCognitoIdentityProviderService.AdminDeleteUser")
	credentials, err := deleter.credentials.Retrieve(ctx)
	if err != nil {
		return errCognitoDelete
	}
	sum := sha256.Sum256(payload)
	if err := v4.NewSigner().SignHTTP(ctx, credentials, request, hex.EncodeToString(sum[:]),
		"cognito-idp", deleter.region, deleter.now()); err != nil {
		return errCognitoDelete
	}
	response, err := deleter.client.Do(request)
	if err != nil {
		return errCognitoDelete
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, cognitoDeleteResponseLimit+1))
	if err != nil || len(body) > cognitoDeleteResponseLimit {
		return errCognitoDelete
	}
	if response.StatusCode == http.StatusOK {
		return nil
	}
	if response.StatusCode == http.StatusBadRequest || response.StatusCode == http.StatusNotFound {
		var failure struct {
			Type string `json:"__type"`
		}
		if json.Unmarshal(body, &failure) == nil {
			kind := failure.Type
			if index := strings.LastIndex(kind, "#"); index >= 0 {
				kind = kind[index+1:]
			}
			if kind == "UserNotFoundException" {
				return nil
			}
		}
	}
	return errCognitoDelete
}
