package blob

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// mockS3 records calls so the driver can be tested without any network.
type mockS3 struct {
	putInput    *s3.PutObjectInput
	getInput    *s3.GetObjectInput
	deleteInput *s3.DeleteObjectInput
	getErr      error
	body        io.ReadCloser
}

func (m *mockS3) PutObject(_ context.Context, params *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	m.putInput = params
	return &s3.PutObjectOutput{}, nil
}

func (m *mockS3) GetObject(_ context.Context, params *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	m.getInput = params
	if m.getErr != nil {
		return nil, m.getErr
	}
	return &s3.GetObjectOutput{Body: m.body}, nil
}

func (m *mockS3) DeleteObject(_ context.Context, params *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	m.deleteInput = params
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3PutUsesPrivateEncryptedObject(t *testing.T) {
	client := &mockS3{}
	store, err := NewS3(S3Config{Bucket: "private-bucket", Region: "ap-southeast-1", Client: client})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	payload := "image-bytes"
	if err := store.Put(context.Background(), "uploads/7/20261106/abc.jpg",
		strings.NewReader(payload), int64(len(payload)), "image/jpeg"); err != nil {
		t.Fatalf("put: %v", err)
	}
	input := client.putInput
	if aws.ToString(input.Bucket) != "private-bucket" {
		t.Fatalf("bucket = %q", aws.ToString(input.Bucket))
	}
	if aws.ToString(input.Key) != "uploads/7/20261106/abc.jpg" {
		t.Fatalf("key = %q", aws.ToString(input.Key))
	}
	if input.ServerSideEncryption != types.ServerSideEncryptionAes256 {
		t.Fatalf("encryption = %q, want AES256", input.ServerSideEncryption)
	}
	if aws.ToString(input.ContentType) != "image/jpeg" {
		t.Fatalf("content type = %q", aws.ToString(input.ContentType))
	}
	if input.ACL != "" {
		t.Fatalf("the driver must not set an ACL, got %q", input.ACL)
	}
	if aws.ToInt64(input.ContentLength) != int64(len(payload)) {
		t.Fatalf("content length = %d", aws.ToInt64(input.ContentLength))
	}
}

func TestS3PrefixAndCustomNamespace(t *testing.T) {
	client := &mockS3{}
	store, err := NewS3(S3Config{Bucket: "bucket", Region: "ap-southeast-1", Prefix: "prod", Client: client})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	if err := store.Put(context.Background(), "uploads/7/a.jpg", strings.NewReader("x"), 1, "image/jpeg"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got := aws.ToString(client.putInput.Key); got != "prod/7/a.jpg" {
		t.Fatalf("key = %q", got)
	}
}

func TestS3GetDeleteAndMissing(t *testing.T) {
	body := io.NopCloser(strings.NewReader("payload"))
	client := &mockS3{body: body}
	store, err := NewS3(S3Config{Bucket: "bucket", Client: client})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	reader, err := store.Get(context.Background(), "uploads/7/a.jpg")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	got, _ := io.ReadAll(reader)
	reader.Close()
	if string(got) != "payload" {
		t.Fatalf("read %q", got)
	}
	if aws.ToString(client.getInput.Key) != "uploads/7/a.jpg" {
		t.Fatalf("get key = %q", aws.ToString(client.getInput.Key))
	}
	if err := store.Delete(context.Background(), "uploads/7/a.jpg"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if aws.ToString(client.deleteInput.Key) != "uploads/7/a.jpg" {
		t.Fatalf("delete key = %q", aws.ToString(client.deleteInput.Key))
	}

	missingClient := &mockS3{getErr: &types.NoSuchKey{}}
	missingStore, err := NewS3(S3Config{Bucket: "bucket", Client: missingClient})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	if _, err := missingStore.Get(context.Background(), "uploads/7/a.jpg"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestS3RejectsTraversalAndMissingConfig(t *testing.T) {
	client := &mockS3{}
	store, err := NewS3(S3Config{Bucket: "bucket", Client: client})
	if err != nil {
		t.Fatalf("new s3: %v", err)
	}
	if err := store.Put(context.Background(), "uploads/../../x", strings.NewReader("x"), 1, "image/jpeg"); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("expected ErrInvalidKey, got %v", err)
	}
	if client.putInput != nil {
		t.Fatal("an invalid key must not reach S3")
	}
	if _, err := NewS3(S3Config{}); err == nil {
		t.Fatal("expected a missing bucket error")
	}
	if _, err := NewS3(S3Config{Bucket: "bucket"}); err == nil {
		t.Fatal("expected a missing client error")
	}
	var expirer Expirer = store
	if expirer.DeleteOnExpire() {
		t.Fatal("S3 must rely on the bucket lifecycle rule")
	}
}
