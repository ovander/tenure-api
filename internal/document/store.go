package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	// PutQuarantine streams r, of unknown size, under key without retention,
	// so it can be deleted after the scan; it returns the object version.
	PutQuarantine(ctx context.Context, key string, r io.Reader) (string, error)
	// Open reads one version of key.
	Open(ctx context.Context, key, version string) (io.ReadCloser, error)
	// CopyLocked copies one version of src to dst with compliance retention
	// until retainUntil, verifies the copy hashes to sha256hex, and returns
	// the version of dst.
	CopyLocked(ctx context.Context, src, srcVersion, dst, contentType, sha256hex string, retainUntil time.Time) (string, error)
	// Delete removes one version of key (a quarantined upload).
	Delete(ctx context.Context, key, version string) error
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

// PutQuarantine implements Store.
func (s *S3) PutQuarantine(ctx context.Context, key string, r io.Reader) (string, error) {
	info, err := s.Client.PutObject(ctx, s.Bucket, key, r, -1, minio.PutObjectOptions{
		ContentType: "application/octet-stream", PartSize: 5 << 20})
	if err != nil {
		return "", fmt.Errorf("document: quarantine %s: %w", key, err)
	}
	return info.VersionID, nil
}

// Open implements Store.
func (s *S3) Open(ctx context.Context, key, version string) (io.ReadCloser, error) {
	obj, err := s.Client.GetObject(ctx, s.Bucket, key, minio.GetObjectOptions{VersionID: version})
	if err != nil {
		return nil, fmt.Errorf("document: open %s: %w", key, err)
	}
	return obj, nil
}

// CopyLocked implements Store.
func (s *S3) CopyLocked(ctx context.Context, src, srcVersion, dst, contentType, sha256hex string, retainUntil time.Time) (string, error) {
	info, err := s.Client.CopyObject(ctx, minio.CopyDestOptions{Bucket: s.Bucket, Object: dst,
		Mode: minio.Compliance, RetainUntilDate: retainUntil.UTC(), ReplaceMetadata: true,
		UserMetadata: map[string]string{"sha256": sha256hex, "Content-Type": contentType}},
		minio.CopySrcOptions{Bucket: s.Bucket, Object: src, VersionID: srcVersion})
	if err != nil {
		return "", fmt.Errorf("document: archive %s: %w", dst, err)
	}
	obj, err := s.Open(ctx, dst, info.VersionID)
	if err != nil {
		return "", err
	}
	defer func() { _ = obj.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, obj); err != nil {
		return "", fmt.Errorf("document: verify %s: %w", dst, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != sha256hex {
		return "", fmt.Errorf("%w: %s", ErrChecksum, dst)
	}
	return info.VersionID, nil
}

// Delete implements Store.
func (s *S3) Delete(ctx context.Context, key, version string) error {
	return s.Client.RemoveObject(ctx, s.Bucket, key, minio.RemoveObjectOptions{VersionID: version})
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
