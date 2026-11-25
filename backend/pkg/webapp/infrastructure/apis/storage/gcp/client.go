package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/domain/file"
)

// ServiceAccountCredentials represents the structure of a GCP service account key file
type ServiceAccountCredentials struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	PrivateKey              string `json:"private_key"`
	ClientEmail             string `json:"client_email"`
	ClientID                string `json:"client_id"`
	AuthURI                 string `json:"auth_uri"`
	TokenURI                string `json:"token_uri"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	ClientX509CertURL       string `json:"client_x509_cert_url"`
}

// GCPStorageClient implements the storage.StorageClient interface for Google Cloud Storage
type GCPStorageClient struct {
	client     *gcs.Client
	bucketName string
	projectID  string
	// For signed URL generation
	serviceAccountEmail string
	privateKey          []byte
}

// NewGCPStorageClient creates a new GCP storage client
func NewGCPStorageClient(ctx context.Context, bucketName, projectID string, credentialsFile string) (*GCPStorageClient, error) {
	if credentialsFile != "" {
		// Read the credentials file and use NewGCPStorageClientWithCredentials internally
		credentialsData, err := os.ReadFile(credentialsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read credentials file: %w", err)
		}
		return NewGCPStorageClientWithCredentials(ctx, bucketName, projectID, credentialsData)
	}

	// No credentials file provided, use default credentials
	client, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	return &GCPStorageClient{
		client:     client,
		bucketName: bucketName,
		projectID:  projectID,
		// No service account credentials for signed URL support when using default credentials
	}, nil
}

// NewGCPStorageClientWithCredentials creates a new GCP storage client with service account credentials
func NewGCPStorageClientWithCredentials(ctx context.Context, bucketName, projectID string, credentialsJSON []byte) (*GCPStorageClient, error) {
	opts := []option.ClientOption{
		option.WithCredentialsJSON(credentialsJSON),
	}

	// Parse the credentials JSON to extract service account info for signed URL support
	var creds ServiceAccountCredentials
	if err := json.Unmarshal(credentialsJSON, &creds); err != nil {
		return nil, fmt.Errorf("failed to parse credentials JSON: %w", err)
	}

	client, err := gcs.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	return &GCPStorageClient{
		client:              client,
		bucketName:          bucketName,
		projectID:           projectID,
		serviceAccountEmail: creds.ClientEmail,
		privateKey:          []byte(creds.PrivateKey),
	}, nil
}

// NewGCPStorageClientWithSignedURLSupport creates a new GCP storage client with signed URL support
func NewGCPStorageClientWithSignedURLSupport(ctx context.Context, bucketName, projectID, serviceAccountEmail string, privateKey []byte, credentialsFile string) (*GCPStorageClient, error) {
	var opts []option.ClientOption
	if credentialsFile != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsFile))
	}

	client, err := gcs.NewClient(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCS client: %w", err)
	}

	return &GCPStorageClient{
		client:              client,
		bucketName:          bucketName,
		projectID:           projectID,
		serviceAccountEmail: serviceAccountEmail,
		privateKey:          privateKey,
	}, nil
}

// StoreWithOptions stores a blob file with advanced options
func (g *GCPStorageClient) StoreWithOptions(ctx context.Context, name string, content io.Reader, options *storage.StorageOptions) (string, error) {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	writer := obj.NewWriter(ctx)

	// Set content type if provided
	if options != nil && options.ContentType != "" {
		writer.ContentType = options.ContentType
	}

	// Set cache control if provided
	if options != nil && options.CacheControl != "" {
		writer.CacheControl = options.CacheControl
	}

	// Set content disposition if provided
	if options != nil && options.ContentDisposition != "" {
		writer.ContentDisposition = options.ContentDisposition
	}

	// Set content encoding if provided
	if options != nil && options.ContentEncoding != "" {
		writer.ContentEncoding = options.ContentEncoding
	}

	// Set metadata if provided
	if options != nil && options.Metadata != nil {
		writer.Metadata = options.Metadata
	}

	// Copy content to writer
	if _, err := io.Copy(writer, content); err != nil {
		writer.Close()
		return "", fmt.Errorf("failed to copy content: %w", err)
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close writer: %w", err)
	}

	// Return the public URL (if bucket is public) or construct a URL
	url := fmt.Sprintf("gs://%s/%s", g.bucketName, name)
	return url, nil
}

// Retrieve retrieves a blob file by name
func (g *GCPStorageClient) Retrieve(ctx context.Context, name string) (io.ReadCloser, error) {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	reader, err := obj.NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create reader: %w", err)
	}
	return reader, nil
}

// GetSignedURL generates a signed URL for the given file name
func (g *GCPStorageClient) GetSignedURL(ctx context.Context, name string, expiration time.Duration) (string, error) {
	// Check if object exists
	exists, err := g.Exists(ctx, name)
	if err != nil {
		return "", fmt.Errorf("failed to check object existence: %w", err)
	}
	if !exists {
		return "", fmt.Errorf("object does not exist: %s", name)
	}

	// If we have service account credentials, generate a proper signed URL
	if g.serviceAccountEmail != "" && len(g.privateKey) > 0 {
		opts := &gcs.SignedURLOptions{
			GoogleAccessID: g.serviceAccountEmail,
			PrivateKey:     g.privateKey,
			Method:         "GET",
			Expires:        time.Now().Add(expiration),
		}

		url, err := gcs.SignedURL(g.bucketName, name, opts)
		if err != nil {
			return "", fmt.Errorf("failed to generate signed URL: %w", err)
		}
		return url, nil
	}

	// Fallback to public URL (this assumes the bucket is public)
	// In production, you should always use proper signed URLs
	url := fmt.Sprintf("https://storage.googleapis.com/%s/%s", g.bucketName, name)
	return url, nil
}

// Delete removes a blob file by name
func (g *GCPStorageClient) Delete(ctx context.Context, name string) error {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	if err := obj.Delete(ctx); err != nil {
		return fmt.Errorf("failed to delete object: %w", err)
	}
	return nil
}

// Exists checks if a blob file exists by name
func (g *GCPStorageClient) Exists(ctx context.Context, name string) (bool, error) {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	_, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcs.ErrObjectNotExist) {
			return false, nil
		}
		// Check for googleapi.Error with Code 404
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 404 {
			return false, nil
		}
		return false, fmt.Errorf("failed to check object existence: %w", err)
	}
	return true, nil
}

// GetFileInfo returns information about a stored file
func (g *GCPStorageClient) GetFileInfo(ctx context.Context, name string) (*file.FileInfo, error) {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if errors.Is(err, gcs.ErrObjectNotExist) {
			return nil, nil
		}
		// Check for googleapi.Error with Code 404
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code == 404 {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get object attributes: %w", err)
	}

	fileInfo := &file.FileInfo{
		Name:         attrs.Name,
		FilePath:     attrs.Name, // Use the same name as file path for GCS
		Size:         attrs.Size,
		ContentType:  attrs.ContentType,
		LastModified: attrs.Updated,
		ETag:         attrs.Etag,
		Metadata:     attrs.Metadata,
	}

	return fileInfo, nil
}

// SetFileMetadata updates the metadata of an existing file
func (g *GCPStorageClient) SetFileMetadata(ctx context.Context, name string, metadata map[string]string) error {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)

	// Update the object with new metadata
	if _, err := obj.Update(ctx, gcs.ObjectAttrsToUpdate{
		Metadata: metadata,
	}); err != nil {
		return fmt.Errorf("failed to update object metadata: %w", err)
	}

	return nil
}

// GetFileMetadata retrieves the metadata of a file
func (g *GCPStorageClient) GetFileMetadata(ctx context.Context, name string) (map[string]string, error) {
	bucket := g.client.Bucket(g.bucketName)
	obj := bucket.Object(name)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if err == gcs.ErrObjectNotExist {
			return nil, fmt.Errorf("file does not exist: %s", name)
		}
		return nil, fmt.Errorf("failed to get object attributes: %w", err)
	}

	return attrs.Metadata, nil
}

// Close closes the GCS client
func (g *GCPStorageClient) Close() error {
	return g.client.Close()
}

// listObjects lists objects in the bucket (helper method for debugging)
func (g *GCPStorageClient) listObjects(ctx context.Context, prefix string) ([]string, error) {
	var objects []string
	bucket := g.client.Bucket(g.bucketName)
	it := bucket.Objects(ctx, &gcs.Query{
		Prefix: prefix,
	})

	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("failed to iterate objects: %w", err)
		}
		objects = append(objects, attrs.Name)
	}

	return objects, nil
}

// GetStorageType returns the storage type identifier
func (g *GCPStorageClient) GetStorageType() string {
	return "gcp"
}
