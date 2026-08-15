package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/alikilicw/filecosystem-be/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	bucketRetryDelay  = 2 * time.Second
	bucketWaitTimeout = 60 * time.Second
)

// Storage wraps Amazon S3 (or any S3-compatible store when an endpoint is set).
type Storage struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	ttl     time.Duration
}

func New(cfg config.S3Config) *Storage {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")

	opts := func(endpoint string) s3.Options {
		o := s3.Options{
			Region:       cfg.Region,
			Credentials:  creds,
		}
		// Empty endpoint means the SDK talks to real AWS S3 for the region.
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
		return o
	}

	presignEndpoint := cfg.PublicEndpoint
	if presignEndpoint == "" {
		presignEndpoint = cfg.Endpoint
	}

	client := s3.New(opts(cfg.Endpoint))
	publicClient := s3.New(opts(presignEndpoint))

	return &Storage{
		client:  client,
		presign: s3.NewPresignClient(publicClient),
		bucket:  cfg.Bucket,
		ttl:     cfg.PresignTTL,
	}
}

// EnsureBucket verifies the configured bucket is reachable. The bucket itself
// is expected to already exist in AWS.
func (s *Storage) EnsureBucket(ctx context.Context) error {
	deadline := time.Now().Add(bucketWaitTimeout)
	for {
		_, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)})
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("head bucket %q: %w", s.bucket, err)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(bucketRetryDelay):
		}
	}
}

func (s *Storage) Put(ctx context.Context, key string, body io.Reader, size int64, contentType string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          body,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put object %s: %w", key, err)
	}
	return nil
}

func (s *Storage) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get object %s: %w", key, err)
	}
	return out.Body, nil
}

func (s *Storage) Delete(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("delete object %s: %w", key, err)
	}
	return nil
}

// PresignGet returns a time-limited download URL. A non-empty downloadName
// makes the browser save the file under that name instead of the storage key.
func (s *Storage) PresignGet(ctx context.Context, key, downloadName string) (string, error) {
	in := &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}
	if downloadName != "" {
		in.ResponseContentDisposition = aws.String(
			fmt.Sprintf("attachment; filename*=UTF-8''%s", url.PathEscape(downloadName)),
		)
	}

	req, err := s.presign.PresignGetObject(ctx, in, s3.WithPresignExpires(s.ttl))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return req.URL, nil
}

func (s *Storage) PresignTTL() time.Duration { return s.ttl }
