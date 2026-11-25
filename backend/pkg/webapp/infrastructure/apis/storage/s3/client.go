// Package s3 implements the storage port against any S3-compatible object
// store. Providers that are not AWS (a location code as the region, path-style
// addressing) are why the two non-obvious knobs below exist.
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/agentpkg"
)

// Config holds what is needed to reach a bucket.
type Config struct {
	Endpoint     string // e.g. https://s3.example.com
	Region       string // some providers use a location code here, e.g. eu-1
	Bucket       string
	AccessKey    string
	SecretKey    string
	UsePathStyle bool
	// PartSizeBytes is the multipart chunk size. An OS image is several GB and
	// arrives as a stream of unknown length, so it MUST go up as multipart;
	// the part size is what bounds the uploader's memory (concurrency × part).
	PartSizeBytes int64
	// UploadConcurrency is how many parts are in flight per upload.
	UploadConcurrency int
}

// S3StorageClient implements storage.StorageClient against an S3 API.
type S3StorageClient struct {
	client   *awss3.Client
	presign  *awss3.PresignClient
	uploader *manager.Uploader
	bucket   string
	logger   *slog.Logger
}

var _ storage.StorageClient = (*S3StorageClient)(nil)

// NewS3StorageClient builds a client. It does NOT contact the bucket — see
// CheckAccess for that, which main.go calls separately so a storage outage is
// reported as a readiness failure rather than a boot loop.
func NewS3StorageClient(cfg Config, logger *slog.Logger) (*S3StorageClient, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("s3 storage: bucket is required")
	}
	if cfg.Endpoint == "" {
		return nil, errors.New("s3 storage: endpoint is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		// Fail loudly rather than falling through to the SDK's ambient
		// credential chain: on a host with no AWS config that chain ends in a
		// slow IMDS probe and an error that says nothing about our config.
		return nil, errors.New("s3 storage: access key and secret key are required")
	}
	if logger == nil {
		panic("s3.NewS3StorageClient: logger is required")
	}
	partSize := cfg.PartSizeBytes
	if partSize < manager.MinUploadPartSize {
		partSize = 16 << 20 // 16 MiB
	}
	concurrency := cfg.UploadConcurrency
	if concurrency <= 0 {
		concurrency = 3
	}

	client := awss3.New(awss3.Options{
		Region:       cfg.Region,
		BaseEndpoint: aws.String(cfg.Endpoint),
		UsePathStyle: cfg.UsePathStyle,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKey, cfg.SecretKey, "",
		),
		// Newer aws-sdk-go-v2 releases compute and send an additional
		// x-amz-checksum-* header on every PutObject by default. Several
		// S3-compatible stores reject the request outright, and the resulting
		// error names the header rather than the cause, so it reads like a
		// credential or signing problem. "WhenRequired" keeps checksums for the
		// operations that genuinely need them and off everywhere else.
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})

	return &S3StorageClient{
		client:  client,
		presign: awss3.NewPresignClient(client),
		uploader: manager.NewUploader(client, func(u *manager.Uploader) {
			u.PartSize = partSize
			u.Concurrency = concurrency
		}),
		bucket: cfg.Bucket,
		logger: logger.With("service", "s3_storage", "bucket", cfg.Bucket),
	}, nil
}

// objectKey normalises a stored name into an object key.
//
// The local backend joins the name onto a base directory and has to defend
// against traversal escaping it. There is no directory here — a key is just a
// string — but the same names must produce the same layout on both backends, so
// that an install can switch and still find "<account>/<image>.img". Traversal
// is still refused: a key containing ".." is not dangerous to us, but it is
// dangerous to anything that later mirrors the bucket onto a filesystem.
func objectKey(name string) (string, error) {
	key := strings.TrimPrefix(strings.TrimPrefix(name, "/"), "\\")
	key = path.Clean(key)
	if key == "." || key == "" {
		return "", errors.New("invalid object name: empty")
	}
	if key == ".." || strings.HasPrefix(key, "../") || strings.Contains(key, "/../") {
		return "", errors.New("invalid object name: contains directory traversal")
	}
	return key, nil
}

// StoreWithOptions uploads a stream. Length is not known in advance (an image
// may be transcoded on the way in), so this always goes through the multipart
// uploader.
func (s *S3StorageClient) StoreWithOptions(ctx context.Context, name string, content io.Reader, options *storage.StorageOptions) (string, error) {
	key, err := objectKey(name)
	if err != nil {
		return "", err
	}
	input := &awss3.PutObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
		Body:   content,
	}
	if options != nil {
		if options.ContentType != "" {
			input.ContentType = aws.String(options.ContentType)
		}
		if options.CacheControl != "" {
			input.CacheControl = aws.String(options.CacheControl)
		}
		if options.ContentDisposition != "" {
			input.ContentDisposition = aws.String(options.ContentDisposition)
		}
		if options.ContentEncoding != "" {
			input.ContentEncoding = aws.String(options.ContentEncoding)
		}
		if options.StorageClass != "" {
			input.StorageClass = types.StorageClass(options.StorageClass)
		}
		if len(options.Metadata) > 0 {
			input.Metadata = options.Metadata
		}
	}
	if _, err := s.uploader.Upload(ctx, input); err != nil {
		return "", fmt.Errorf("failed to upload object %q: %w", key, err)
	}
	return key, nil
}

// Retrieve streams an object back. The caller closes it.
func (s *S3StorageClient) Retrieve(ctx context.Context, name string) (io.ReadCloser, error) {
	key, err := objectKey(name)
	if err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, fmt.Errorf("file not found: %s", name)
		}
		return nil, fmt.Errorf("failed to read object %q: %w", key, err)
	}
	return out.Body, nil
}

// GetSignedURL returns a time-limited URL for the object.
//
// Unlike the local backend — whose "signed" URL is just the base URL with the
// path appended, and whose access control is therefore the CDN handler's own
// HMAC — this is a real S3 presigned URL: the bucket stays private and the URL
// carries its own authorisation.
func (s *S3StorageClient) GetSignedURL(ctx context.Context, name string, expiration time.Duration) (string, error) {
	key, err := objectKey(name)
	if err != nil {
		return "", err
	}
	// Presigning does not contact the service, so an URL would be produced for
	// an object that does not exist. The local backend checks; match it, or the
	// two backends disagree about whether a missing file is an error here or a
	// 404 later.
	exists, err := s.Exists(ctx, name)
	if err != nil {
		return "", err
	}
	if !exists {
		return "", fmt.Errorf("file does not exist: %s", name)
	}
	req, err := s.presign.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, func(o *awss3.PresignOptions) { o.Expires = expiration })
	if err != nil {
		return "", fmt.Errorf("failed to presign object %q: %w", key, err)
	}
	return req.URL, nil
}

// Delete removes an object. A missing object is not an error, matching the
// local backend and keeping deletes idempotent.
func (s *S3StorageClient) Delete(ctx context.Context, name string) error {
	key, err := objectKey(name)
	if err != nil {
		return err
	}
	if _, err := s.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete object %q: %w", key, err)
	}
	return nil
}

// Exists reports whether the object is present.
func (s *S3StorageClient) Exists(ctx context.Context, name string) (bool, error) {
	key, err := objectKey(name)
	if err != nil {
		return false, err
	}
	if _, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to stat object %q: %w", key, err)
	}
	return true, nil
}

// GetFileInfo returns metadata, or nil when the object is absent (the port's
// contract, and what the image service checks).
func (s *S3StorageClient) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	key, err := objectKey(name)
	if err != nil {
		return nil, err
	}
	head, err := s.client.HeadObject(ctx, &awss3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to stat object %q: %w", key, err)
	}
	info := &file.FileInfo{
		Name:     path.Base(key),
		FilePath: name,
		Metadata: head.Metadata,
	}
	if head.ContentLength != nil {
		info.Size = *head.ContentLength
	}
	if head.LastModified != nil {
		info.LastModified = *head.LastModified
	}
	if head.ContentType != nil {
		info.ContentType = *head.ContentType
	}
	return info, nil
}

// GetStorageType identifies this backend. The CDN handler and account-purge
// tooling both branch on it, so it must not change casually.
func (s *S3StorageClient) GetStorageType() string { return "s3" }

// ListAgentVersions lists the agent packages under basePath. Parsing and
// ordering are shared with the local backend via agentpkg.
func (s *S3StorageClient) ListAgentVersions(basePath string) ([]string, error) {
	names, err := s.agentFileNames(basePath)
	if err != nil {
		return nil, err
	}
	return agentpkg.Versions(names), nil
}

// ListAgentVersionsByArch is ListAgentVersions per architecture.
func (s *S3StorageClient) ListAgentVersionsByArch(basePath string) (map[string][]string, error) {
	names, err := s.agentFileNames(basePath)
	if err != nil {
		return nil, err
	}
	return agentpkg.VersionsByArch(names), nil
}

func (s *S3StorageClient) agentFileNames(basePath string) ([]string, error) {
	prefix := strings.Trim(basePath, "/")
	if prefix != "" {
		prefix += "/"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var names []string
	paginator := awss3.NewListObjectsV2Paginator(s.client, &awss3.ListObjectsV2Input{
		Bucket: aws.String(s.bucket),
		Prefix: aws.String(prefix),
	})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list agent packages: %w", err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			names = append(names, path.Base(*obj.Key))
		}
	}
	return names, nil
}

// CheckAccess proves the configured credentials can reach the bucket. Called
// from the readiness probe, where it replaces the local backend's free-space
// check: for a remote store "can I reach it" is the question that matters, and
// a statfs of a directory nothing writes to any more would be a probe that
// always passes.
func (s *S3StorageClient) CheckAccess(ctx context.Context) error {
	if _, err := s.client.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(s.bucket),
	}); err != nil {
		return fmt.Errorf("bucket %q is not reachable: %w", s.bucket, err)
	}
	return nil
}

// isNotFound recognises absence across the several shapes the SDK reports it
// in: typed NoSuchKey/NotFound on GetObject, and a bare 404 API error on
// HeadObject, which carries no typed error at all.
func isNotFound(err error) bool {
	var noKey *types.NoSuchKey
	if errors.As(err, &noKey) {
		return true
	}
	var notFound *types.NotFound
	if errors.As(err, &notFound) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchKey", "NotFound", "404":
			return true
		}
	}
	return false
}
