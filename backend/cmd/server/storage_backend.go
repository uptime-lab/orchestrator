package main

import (
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
	s3_storage "github.com/uptime-lab/orchestrator/backend/pkg/webapp/infrastructure/apis/storage/s3"
)

// storageBackendKind is the ONE decision of which storage backend a
// configuration selects (storage.TypeS3, storage.TypeGCP or
// storage.TypeLocal). The server start and `--import-agent-release` both ask
// it: an import that chose differently from the server would publish a
// release into a store the CDN never reads, and report success.
//
// STORAGE_TYPE "auto" (also the empty default) picks S3 when both bucket and
// endpoint are set, else GCP when its bucket and project are, else local.
// S3 is probed before GCP only so that an install that configured both gets
// a deterministic answer; nothing here prefers one product. Any other value
// than gcp, s3 or auto means local.
func storageBackendKind(cfg *config.Config) string {
	storageType := cfg.Storage.Type
	if storageType == "" {
		storageType = "auto"
	}
	switch storageType {
	case "gcp":
		return storage.TypeGCP
	case "s3":
		return storage.TypeS3
	case "auto":
		if cfg.Storage.S3.Bucket != "" && cfg.Storage.S3.Endpoint != "" {
			return storage.TypeS3
		}
		if cfg.Storage.GCP.BucketName != "" && cfg.Storage.GCP.ProjectID != "" {
			return storage.TypeGCP
		}
	}
	return storage.TypeLocal
}

// s3ConfigFrom maps the server configuration onto the S3 adapter's.
func s3ConfigFrom(cfg *config.Config) s3_storage.Config {
	return s3_storage.Config{
		Endpoint:          cfg.Storage.S3.Endpoint,
		Region:            cfg.Storage.S3.Region,
		Bucket:            cfg.Storage.S3.Bucket,
		AccessKey:         cfg.Storage.S3.AccessKey,
		SecretKey:         cfg.Storage.S3.SecretKey,
		UsePathStyle:      cfg.Storage.S3.UsePathStyle,
		PartSizeBytes:     cfg.Storage.S3.PartSizeBytes,
		UploadConcurrency: cfg.Storage.S3.UploadConcurrency,
	}
}
