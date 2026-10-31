package storage

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
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
	cdnBase string
}

// PresignedUpload is the ticket a client uses to POST one object straight into
// the bucket as a multipart form: every entry in Fields goes into the form,
// followed by the file itself as the last field, named "file".
type PresignedUpload struct {
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ExpiresAt time.Time         `json:"expires_at"`
}

func New(cfg config.S3Config) *Storage {
	creds := credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")

	opts := func(endpoint string) s3.Options {
		o := s3.Options{
			Region:      cfg.Region,
			Credentials: creds,
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
		cdnBase: cfg.CDNBaseURL,
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

// PresignPost issues an upload ticket for exactly one object. The signed policy
// pins the key, the content type and the exact size in bytes, and S3 enforces
// all three: a client cannot redirect the upload, store an unaccepted type, or
// send more (or fewer) bytes than it declared. A presigned PUT cannot limit
// the size, which is why uploads use POST.
//
// The ticket can be reused until it expires, but only to overwrite the same
// key with a body of the same size.
func (s *Storage) PresignPost(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (PresignedUpload, error) {
	req, err := s.presign.PresignPostObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *s3.PresignPostOptions) {
		o.Expires = ttl
		o.Conditions = []any{
			map[string]string{"Content-Type": contentType},
			[]any{"content-length-range", size, size},
		}
	})
	if err != nil {
		return PresignedUpload{}, fmt.Errorf("presign post %s: %w", key, err)
	}

	// The policy requires a Content-Type field, so hand it out with the rest.
	fields := make(map[string]string, len(req.Values)+1)
	for k, v := range req.Values {
		fields[k] = v
	}
	fields["Content-Type"] = contentType

	return PresignedUpload{
		URL:       req.URL,
		Fields:    fields,
		ExpiresAt: time.Now().UTC().Add(ttl),
	}, nil
}

// Head reports what is actually stored under a key without fetching the body.
func (s *Storage) Head(ctx context.Context, key string) (size int64, contentType string, err error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return 0, "", fmt.Errorf("head object %s: %w", key, err)
	}
	return aws.ToInt64(out.ContentLength), aws.ToString(out.ContentType), nil
}

// ObjectURL returns a link that opens a stored object in the browser.
//
// With a CDN configured it is a plain CDN link: the bucket stays private
// behind the CDN, and the random UUID in every key keeps links unguessable.
// Without a CDN it falls back to a presigned S3 URL.
func (s *Storage) ObjectURL(ctx context.Context, key string) (string, error) {
	if s.cdnBase != "" {
		return s.cdnBase + "/" + escapeKey(key), nil
	}
	return s.presignGet(ctx, key, "")
}

// DownloadURL returns a link that makes the browser save the object under
// downloadName instead of opening it.
//
// On the CDN this is the ObjectURL plus "?download=1"; the CloudFront Function
// in infra/cloudfront turns that into an attachment Content-Disposition. The
// query string is not part of the cache key, so both links share one cached
// copy. Without a CDN the presigned URL overrides the header itself.
func (s *Storage) DownloadURL(ctx context.Context, key, downloadName string) (string, error) {
	if s.cdnBase != "" {
		return s.cdnBase + "/" + escapeKey(key) + "?download=1", nil
	}
	return s.presignGet(ctx, key, downloadName)
}

// escapeKey escapes each path segment of a key but keeps the slashes.
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func (s *Storage) presignGet(ctx context.Context, key, downloadName string) (string, error) {
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
