package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func TestAWSCognitoDeletionSignsStoredIdentity(t *testing.T) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	credentials := aws.Credentials{
		AccessKeyID: "test-" + hex.EncodeToString(key[:8]), SecretAccessKey: hex.EncodeToString(key),
		SessionToken: hex.EncodeToString(key[8:24]), Source: "unit-test",
	}
	calls := 0
	client := &http.Client{
		Timeout: time.Minute,
		Transport: cognitoTestTransport(func(request *http.Request) (*http.Response, error) {
			calls++
			if request.URL.String() != "https://cognito-idp.ap-southeast-1.amazonaws.com/" ||
				request.Method != http.MethodPost ||
				request.Header.Get("Content-Type") != "application/x-amz-json-1.1" ||
				request.Header.Get("X-Amz-Target") != "AWSCognitoIdentityProviderService.AdminDeleteUser" {
				t.Fatal("request does not use Cognito's fixed AdminDeleteUser endpoint/protocol")
			}
			var body map[string]string
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 2 || body["UserPoolId"] != "ap-southeast-1_testPool" ||
				body["Username"] != "stored-provider-username" {
				t.Fatal("unexpected deletion identity")
			}
			authorization := request.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, "AWS4-HMAC-SHA256 ") ||
				!strings.Contains(authorization, "/ap-southeast-1/cognito-idp/aws4_request") ||
				!strings.Contains(authorization, "x-amz-target") ||
				request.Header.Get("X-Amz-Date") == "" ||
				request.Header.Get("X-Amz-Security-Token") != credentials.SessionToken {
				t.Fatal("request is not signed for Cognito with the provided credentials")
			}
			if strings.Contains(authorization, credentials.SecretAccessKey) {
				t.Fatal("SigV4 exposed the secret access key")
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}")), Request: request}, nil
		}),
	}
	deleter, err := NewAWSCognitoDeleter(context.Background(), "ap-southeast-1", "ap-southeast-1_testPool",
		WithCognitoDeleteCredentials(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return credentials, nil
		})), WithCognitoDeleteHTTPClient(client))
	if err != nil {
		t.Fatal(err)
	}
	if err := deleter.DeleteUser(context.Background(), "stored-provider-username"); err != nil || calls != 1 {
		t.Fatalf("provider deletion failed: calls=%d err=%v", calls, err)
	}
	if client.Timeout != time.Minute || client.CheckRedirect != nil {
		t.Fatal("constructor mutated the caller's HTTP client")
	}
	if actual := deleter.(*awsCognitoDeleter); actual.client.Timeout != 10*time.Second {
		t.Fatal("provider timeout was not bounded")
	}
}

func TestAWSCognitoDeletionFailureIsolation(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
		success    bool
	}{
		{"already_deleted", `{"__type":"UserNotFoundException"}`, 400, true},
		{"qualified_already_deleted", `{"__type":"com.amazonaws.cognito#UserNotFoundException"}`, 400, true},
		{"denied", `{"__type":"NotAuthorizedException","message":"private-identity"}`, 400, false},
		{"unavailable", "private-identity", 503, false},
		{"malformed_error", "{", 400, false},
		{"oversized_success", strings.Repeat("x", cognitoDeleteResponseLimit+1), 200, false},
		{"redirect", "", 302, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: cognitoTestTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Host != "cognito-idp.ap-southeast-1.amazonaws.com" {
					t.Fatal("provider redirect was followed")
				}
				header := make(http.Header)
				header.Set("Location", "https://attacker.example/credentials")
				return &http.Response{
					StatusCode: test.status, Header: header,
					Body: io.NopCloser(strings.NewReader(test.body)), Request: request,
				}, nil
			})}
			deleter, err := NewAWSCognitoDeleter(context.Background(), "ap-southeast-1", "ap-southeast-1_testPool",
				WithCognitoDeleteHTTPClient(client),
				WithCognitoDeleteCredentials(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "generated-for-test", SecretAccessKey: t.Name()}, nil
				})))
			if err != nil {
				t.Fatal(err)
			}
			err = deleter.DeleteUser(context.Background(), "stored-provider-username")
			if (err == nil) != test.success || calls != 1 {
				t.Fatalf("unexpected deletion result: err=%v calls=%d", err, calls)
			}
			if err != nil && (strings.Contains(err.Error(), "private-identity") ||
				strings.Contains(err.Error(), "stored-provider-username")) {
				t.Fatal("provider errors expose identity or response details")
			}
		})
	}
}

func TestAWSCognitoDeletionRejectsInvalidConfigurationAndCredentials(t *testing.T) {
	for _, pair := range [][2]string{
		{"ap-southeast-1", "us-east-1_wrongPool"},
		{"https://attacker.example", "https://attacker.example_pool"},
		{"ap-southeast-1", "ap-southeast-1_"},
	} {
		if _, err := NewAWSCognitoDeleter(context.Background(), pair[0], pair[1]); err == nil {
			t.Fatal("invalid region/pool configuration accepted")
		}
	}
	calls := 0
	deleter, err := NewAWSCognitoDeleter(context.Background(), "ap-southeast-1", "ap-southeast-1_testPool",
		WithCognitoDeleteCredentials(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, errors.New("private credentials failure")
		})),
		WithCognitoDeleteHTTPClient(&http.Client{Transport: cognitoTestTransport(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("network must not run")
		})}))
	if err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"", "stored-provider-username"} {
		if err := deleter.DeleteUser(context.Background(), username); !errors.Is(err, errCognitoDelete) {
			t.Fatalf("invalid input/provider was not safely rejected: %v", err)
		}
	}
	if calls != 0 {
		t.Fatal("missing credentials triggered a provider request")
	}
}
