package s3

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestObjectKeyMatchesTheLocalBackendsLayout(t *testing.T) {
	// The same stored name must produce the same layout on both backends, or an
	// install that switches cannot find what it wrote.
	cases := map[string]string{
		"account/image.img":  "account/image.img",
		"/account/image.img": "account/image.img",
		"agent-artifacts/orchestrator-agent_0.4.7_linux_arm64.deb": "agent-artifacts/orchestrator-agent_0.4.7_linux_arm64.deb",
	}
	for in, want := range cases {
		got, err := objectKey(in)
		if err != nil {
			t.Fatalf("objectKey(%q) errored: %v", in, err)
		}
		if got != want {
			t.Errorf("objectKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestObjectKeyRefusesTraversal(t *testing.T) {
	// Not dangerous to a key/value store, but dangerous to anything that later
	// mirrors the bucket onto a filesystem — which an off-host sync would do.
	for _, in := range []string{"../etc/passwd", "account/../../etc/passwd", "..", "", "/"} {
		if _, err := objectKey(in); err == nil {
			t.Errorf("objectKey(%q) was accepted; want refusal", in)
		}
	}
}

func TestIsNotFoundRecognisesEveryShape(t *testing.T) {
	// HeadObject returns a bare 404 API error with no typed error at all, which
	// is why matching only the typed ones silently turned "absent" into "broken".
	if !isNotFound(&types.NoSuchKey{}) {
		t.Error("typed NoSuchKey not recognised")
	}
	if !isNotFound(&types.NotFound{}) {
		t.Error("typed NotFound not recognised")
	}
	if !isNotFound(&smithy.GenericAPIError{Code: "NotFound"}) {
		t.Error("bare 404 API error not recognised")
	}
	if isNotFound(errors.New("connection refused")) {
		t.Error("a transport failure was reported as absence — that would turn an outage into a silent empty result")
	}
	if isNotFound(&smithy.GenericAPIError{Code: "AccessDenied"}) {
		t.Error("AccessDenied was reported as absence — a misconfigured key would read as an empty bucket")
	}
}

func TestNewRefusesIncompleteConfig(t *testing.T) {
	base := Config{
		Endpoint:  "https://s3.example.com",
		Region:    "eu-1",
		Bucket:    "b",
		AccessKey: "ak",
		SecretKey: "sk",
	}
	if _, err := NewS3StorageClient(base, testLogger()); err != nil {
		t.Fatalf("a complete config was refused: %v", err)
	}

	missing := map[string]func(Config) Config{
		"bucket":     func(c Config) Config { c.Bucket = ""; return c },
		"endpoint":   func(c Config) Config { c.Endpoint = ""; return c },
		"access key": func(c Config) Config { c.AccessKey = ""; return c },
		"secret key": func(c Config) Config { c.SecretKey = ""; return c },
	}
	for name, mutate := range missing {
		if _, err := NewS3StorageClient(mutate(base), testLogger()); err == nil {
			t.Errorf("missing %s was accepted; the SDK would then fall through to its ambient "+
				"credential chain and fail with an error that names none of our config", name)
		}
	}
}
