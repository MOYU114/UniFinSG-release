package blob

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// DefaultS3Prefix namespaces every object in the private bucket.
const DefaultS3Prefix = "uploads/"

// s3API is the narrow subset of the SDK the driver uses. Tests substitute a
// mock so no test ever reaches S3.
type s3API interface {
	PutObject(ctx context.Context, params *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(ctx context.Context, params *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(ctx context.Context, params *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

// S3Config configures the private-bucket driver.
type S3Config struct {
	Bucket string
	Region string
	Prefix string
	Client s3API
}

// S3 stores objects in a private bucket with SSE-S3. It never generates public
// or presigned URLs.
type S3 struct {
	bucket string
	prefix string
	client s3API
}

func NewS3(cfg S3Config) (*S3, error) {
	bucket := strings.TrimSpace(cfg.Bucket)
	if bucket == "" {
		return nil, errors.New("blob: S3_BUCKET is required")
	}
	if cfg.Client == nil {
		return nil, errors.New("blob: S3 client is required")
	}
	prefix := strings.TrimSpace(cfg.Prefix)
	if prefix == "" {
		prefix = DefaultS3Prefix
	}
	prefix = strings.TrimLeft(prefix, "/")
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return &S3{bucket: bucket, prefix: prefix, client: cfg.Client}, nil
}

// NewS3FromEnv builds the driver from S3_BUCKET / S3_REGION / S3_PREFIX using
// the default AWS credential chain (for ECS, the task role).
func NewS3FromEnv(ctx context.Context) (*S3, error) {
	bucket := strings.TrimSpace(os.Getenv("S3_BUCKET"))
	region := strings.TrimSpace(os.Getenv("S3_REGION"))
	if bucket == "" {
		return nil, errors.New("blob: S3_BUCKET is required when STORAGE_DRIVER=s3")
	}
	if region == "" {
		return nil, errors.New("blob: S3_REGION is required when STORAGE_DRIVER=s3")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("blob: load AWS config: %w", err)
	}
	return NewS3(S3Config{
		Bucket: bucket, Region: region, Prefix: os.Getenv("S3_PREFIX"),
		Client: s3.NewFromConfig(cfg),
	})
}

// DeleteOnExpire reports that the bucket lifecycle rule removes objects, so
// the hourly sweep only updates the database.
func (s *S3) DeleteOnExpire() bool { return false }

func (s *S3) objectKey(key string) (string, error) {
	relative, err := relativeKey(key)
	if err != nil {
		return "", err
	}
	return s.prefix + relative, nil
}

func (s *S3) Put(ctx context.Context, key string, r io.Reader, size int64, mime string) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	input := &s3.PutObjectInput{
		Bucket:               aws.String(s.bucket),
		Key:                  aws.String(objectKey),
		Body:                 r,
		ContentType:          aws.String(mime),
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	}
	if size > 0 {
		input.ContentLength = aws.Int64(size)
	}
	if _, err := s.client.PutObject(ctx, input); err != nil {
		return fmt.Errorf("blob: put object: %w", err)
	}
	return nil
}

func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return nil, err
	}
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("blob: get object: %w", err)
	}
	return output.Body, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	objectKey, err := s.objectKey(key)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectKey),
	}); err != nil {
		return fmt.Errorf("blob: delete object: %w", err)
	}
	return nil
}
