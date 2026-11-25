package main

import (
	"testing"

	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/config"
	"github.com/uptime-lab/orchestrator/backend/pkg/webapp/application/ports/storage"
)

func TestStorageBackendKind(t *testing.T) {
	withS3 := func(c *config.Config) { c.Storage.S3.Bucket, c.Storage.S3.Endpoint = "b", "e" }
	withGCP := func(c *config.Config) { c.Storage.GCP.BucketName, c.Storage.GCP.ProjectID = "b", "p" }
	cases := []struct {
		name string
		typ  string
		set  []func(*config.Config)
		want string
	}{
		{"empty is auto, nothing set: local", "", nil, storage.TypeLocal},
		{"auto with S3 bucket and endpoint", "auto", []func(*config.Config){withS3}, storage.TypeS3},
		{"auto prefers S3 when both are set", "", []func(*config.Config){withS3, withGCP}, storage.TypeS3},
		{"auto with GCP only", "auto", []func(*config.Config){withGCP}, storage.TypeGCP},
		{"explicit s3", "s3", nil, storage.TypeS3},
		{"explicit gcp", "gcp", nil, storage.TypeGCP},
		{"explicit local ignores buckets", "local", []func(*config.Config){withS3}, storage.TypeLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Storage.Type = tc.typ
			for _, f := range tc.set {
				f(cfg)
			}
			if got := storageBackendKind(cfg); got != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
