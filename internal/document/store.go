package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Store is the write-once archive (RENT-SPEC-005).
type Store interface {
	// Put writes data under key with compliance retention until retainUntil
	// and verifies the stored bytes hash to sha256hex; it returns the object
	// version.
	Put(ctx context.Context, key string, data []byte, contentType, sha256hex string, retainUntil time.Time) (string, error)
	// PresignGet returns a URL valid for ttl that downloads one version of key.
	PresignGet(ctx context.Context, key, version string, ttl time.Duration, params url.Values) (*url.URL, error)
	// CheckLock reports whether the bucket enforces object lock (/readyz).
	CheckLock(ctx context.Context) error
}

// S3 is the Store over an S3-compatible bucket with versioning and object lock
// (Scaleway in production, OD-04; RustFS in development, Q-38).
type S3 struct {
	Client *minio.Client
	Bucket string
}

// NewS3 connects to the bucket.
func NewS3(endpoint, region, bucket, accessKey, secretKey string, secure bool) (*S3, error) {
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure, Region: region})
	if err != nil {
		return nil, err
	}
	return &S3{Client: c, Bucket: bucket}, nil
}

// ErrChecksum reports an object whose stored bytes differ from what was sent.
var ErrChecksum = errors.New("document: stored object does not match its checksum")

// Put implements Store.
func (s *S3) Put(ctx context.Context, key string, data []byte, contentType, sha256hex string, retainUntil time.Time) (string, error) {
	info, err := s.Client.PutObject(ctx, s.Bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType: contentType, SendContentMd5: true, Mode: minio.Compliance, RetainUntilDate: retainUntil.UTC(),
		UserMetadata: map[string]string{"sha256": sha256hex},
	})
	if err != nil {
		return "", fmt.Errorf("document: put %s: %w", key, err)
	}
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{VersionID: info.VersionID})
	if err != nil {
		return "", fmt.Errorf("document: verify %s: %w", key, err)
	}
	defer func() { _ = obj.Close() }()
	back, err := io.ReadAll(obj)
	if err != nil {
		return "", fmt.Errorf("document: verify %s: %w", key, err)
	}
	if SHA256(back) != sha256hex {
		return "", fmt.Errorf("%w: %s", ErrChecksum, key)
	}
	return info.VersionID, nil
}

// PresignGet implements Store.
func (s *S3) PresignGet(ctx context.Context, key, version string, ttl time.Duration, params url.Values) (*url.URL, error) {
	p := url.Values{}
	for k, v := range params {
		p[k] = v
	}
	if version != "" {
		p.Set("versionId", version)
	}
	return s.Client.PresignedGetObject(ctx, s.Bucket, key, ttl, p)
}

// CheckLock implements Store.
func (s *S3) CheckLock(ctx context.Context) error {
	enabled, mode, _, _, err := s.Client.GetObjectLockConfig(ctx, s.Bucket)
	if err != nil {
		return fmt.Errorf("document: bucket %s: object lock: %w", s.Bucket, err)
	}
	if enabled != "Enabled" {
		return fmt.Errorf("document: bucket %s has no object lock", s.Bucket)
	}
	if mode != nil && *mode != minio.Compliance {
		return fmt.Errorf("document: bucket %s default retention is %s, not COMPLIANCE", s.Bucket, *mode)
	}
	return nil
}
